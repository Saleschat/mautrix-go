// Copyright (c) 2023 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package appservice

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"golang.org/x/net/publicsuffix"
	"gopkg.in/yaml.v3"

	"go.mau.fi/zeroconfig"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// EventChannelSize is the size for the Events channel in Appservice instances.
var EventChannelSize = 64
var OTKChannelSize = 4

// Create a blank appservice instance.
func Create() *AppService {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	defaultLogLevel := zerolog.TraceLevel
	return &AppService{
		LogConfig: &zeroconfig.Config{
			MinLevel: &defaultLogLevel,
			Writers: []zeroconfig.WriterConfig{{
				Type:   zeroconfig.WriterTypeStdout,
				Format: zeroconfig.LogFormatPrettyColored,
			}},
		},
		clients:    make(map[id.UserID]*mautrix.Client),
		intents:    make(map[id.UserID]*IntentAPI),
		HTTPClient: &http.Client{Timeout: 180 * time.Second, Jar: jar},
		StateStore: mautrix.NewMemoryStateStore().(StateStore),
		Router:     mux.NewRouter(),
		UserAgent:  mautrix.DefaultUserAgent,
		txnIDC:     NewTransactionIDCache(128),
		Live:       true,
		Ready:      false,
	}
}

// Load an appservice config from a file.
func Load(path string) (*AppService, error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, readErr
	}

	config := Create()
	return config, yaml.Unmarshal(data, config)
}

// QueryHandler handles room alias and user ID queries from the homeserver.
type QueryHandler interface {
	QueryAlias(alias string) bool
	QueryUser(userID id.UserID) bool
}

type QueryHandlerStub struct{}

func (qh *QueryHandlerStub) QueryAlias(alias string) bool {
	return false
}

func (qh *QueryHandlerStub) QueryUser(userID id.UserID) bool {
	return false
}

type WebsocketHandler func(WebsocketCommand) (ok bool, data interface{})

type StateStore interface {
	mautrix.StateStore

	IsRegistered(userID id.UserID) bool
	MarkRegistered(userID id.UserID)

	GetPowerLevel(roomID id.RoomID, userID id.UserID) int
	GetPowerLevelRequirement(roomID id.RoomID, eventType event.Type) int
	HasPowerLevel(roomID id.RoomID, userID id.UserID, eventType event.Type) bool
}

// AppService is the main config for all appservices.
// It also serves as the appservice instance struct.
type AppService struct {
	HomeserverDomain string             `yaml:"homeserver_domain"`
	HomeserverURL    string             `yaml:"homeserver_url"`
	RegistrationPath string             `yaml:"registration"`
	Host             HostConfig         `yaml:"host"`
	LogConfig        *zeroconfig.Config `yaml:"logging"`

	Registration *Registration  `yaml:"-"`
	Log          zerolog.Logger `yaml:"-"`

	txnIDC *TransactionIDCache

	Events         chan *event.Event         `yaml:"-"`
	ToDeviceEvents chan *event.Event         `yaml:"-"`
	DeviceLists    chan *mautrix.DeviceLists `yaml:"-"`
	OTKCounts      chan *mautrix.OTKCount    `yaml:"-"`
	QueryHandler   QueryHandler              `yaml:"-"`
	StateStore     StateStore                `yaml:"-"`

	Router     *mux.Router `yaml:"-"`
	UserAgent  string      `yaml:"-"`
	server     *http.Server
	HTTPClient *http.Client
	botClient  *mautrix.Client
	botIntent  *IntentAPI

	DefaultHTTPRetries int

	Live  bool
	Ready bool

	clients     map[id.UserID]*mautrix.Client
	clientsLock sync.RWMutex
	intents     map[id.UserID]*IntentAPI
	intentsLock sync.RWMutex

	ws                    *websocket.Conn
	wsWriteLock           sync.Mutex
	StopWebsocket         func(error)
	websocketHandlers     map[string]WebsocketHandler
	websocketHandlersLock sync.RWMutex
	websocketRequests     map[int]chan<- *WebsocketCommand
	websocketRequestsLock sync.RWMutex
	websocketRequestID    int32
	// ProcessID is an identifier sent to the websocket proxy for debugging connections
	ProcessID string

	DoublePuppetValue string
	GetProfile        func(userID id.UserID, roomID id.RoomID) *event.MemberEventContent
}

const DoublePuppetKey = "fi.mau.double_puppet_source"

func getDefaultProcessID() string {
	pid := syscall.Getpid()
	uid := syscall.Getuid()
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s-%d-%d", hostname, uid, pid)
}

func (as *AppService) PrepareWebsocket() {
	as.websocketHandlersLock.Lock()
	defer as.websocketHandlersLock.Unlock()
	if as.websocketHandlers == nil {
		as.websocketHandlers = make(map[string]WebsocketHandler, 32)
		as.websocketRequests = make(map[int]chan<- *WebsocketCommand)
	}
}

// HostConfig contains info about how to host the appservice.
type HostConfig struct {
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`
	TLSKey   string `yaml:"tls_key,omitempty"`
	TLSCert  string `yaml:"tls_cert,omitempty"`
}

// Address gets the whole address of the Appservice.
func (hc *HostConfig) Address() string {
	return fmt.Sprintf("%s:%d", hc.Hostname, hc.Port)
}

// Save saves this config into a file at the given path.
func (as *AppService) Save(path string) error {
	data, err := yaml.Marshal(as)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// YAML returns the config in YAML format.
func (as *AppService) YAML() (string, error) {
	data, err := yaml.Marshal(as)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (as *AppService) BotMXID() id.UserID {
	return id.NewUserID(as.Registration.SenderLocalpart, as.HomeserverDomain)
}

func (as *AppService) makeIntent(userID id.UserID) *IntentAPI {
	as.intentsLock.Lock()
	defer as.intentsLock.Unlock()

	intent, ok := as.intents[userID]
	if ok {
		return intent
	}

	localpart, homeserver, err := userID.Parse()
	if err != nil || len(localpart) == 0 || homeserver != as.HomeserverDomain {
		if err != nil {
			as.Log.Error().Err(err).
				Str("user_id", userID.String()).
				Msg("Failed to parse user ID")
		} else if len(localpart) == 0 {
			as.Log.Error().Err(err).
				Str("user_id", userID.String()).
				Msg("Failed to make intent: localpart is empty")
		} else if homeserver != as.HomeserverDomain {
			as.Log.Error().Err(err).
				Str("user_id", userID.String()).
				Str("expected_homeserver", as.HomeserverDomain).
				Msg("Failed to make intent: homeserver doesn't match")
		}
		return nil
	}
	intent = as.NewIntentAPI(localpart)
	as.intents[userID] = intent
	return intent
}

func (as *AppService) Intent(userID id.UserID) *IntentAPI {
	as.intentsLock.RLock()
	intent, ok := as.intents[userID]
	as.intentsLock.RUnlock()
	if !ok {
		return as.makeIntent(userID)
	}
	return intent
}

func (as *AppService) BotIntent() *IntentAPI {
	if as.botIntent == nil {
		as.botIntent = as.makeIntent(as.BotMXID())
	}
	return as.botIntent
}

func (as *AppService) makeClient(userID id.UserID) *mautrix.Client {
	as.clientsLock.Lock()
	defer as.clientsLock.Unlock()

	client, ok := as.clients[userID]
	if ok {
		return client
	}

	client, err := mautrix.NewClient(as.HomeserverURL, userID, as.Registration.AppToken)
	if err != nil {
		as.Log.Error().Err(err).Msg("Failed to create mautrix client instance")
		return nil
	}
	client.UserAgent = as.UserAgent
	client.Syncer = nil
	client.Store = nil
	client.StateStore = as.StateStore
	client.SetAppServiceUserID = true
	client.Log = as.Log.With().Str("as_user_id", client.UserID.String()).Logger()
	client.Client = as.HTTPClient
	client.DefaultHTTPRetries = as.DefaultHTTPRetries
	as.clients[userID] = client
	return client
}

func (as *AppService) Client(userID id.UserID) *mautrix.Client {
	as.clientsLock.RLock()
	client, ok := as.clients[userID]
	as.clientsLock.RUnlock()
	if !ok {
		return as.makeClient(userID)
	}
	return client
}

func (as *AppService) BotClient() *mautrix.Client {
	if as.botClient == nil {
		as.botClient = as.makeClient(as.BotMXID())
	}
	return as.botClient
}

// Init initializes the logger and loads the registration of this appservice.
func (as *AppService) Init() (bool, error) {
	as.Events = make(chan *event.Event, EventChannelSize)
	as.ToDeviceEvents = make(chan *event.Event, EventChannelSize)
	as.OTKCounts = make(chan *mautrix.OTKCount, OTKChannelSize)
	as.DeviceLists = make(chan *mautrix.DeviceLists, EventChannelSize)
	as.QueryHandler = &QueryHandlerStub{}

	as.Router.HandleFunc("/transactions/{txnID}", as.PutTransaction).Methods(http.MethodPut)
	as.Router.HandleFunc("/rooms/{roomAlias}", as.GetRoom).Methods(http.MethodGet)
	as.Router.HandleFunc("/users/{userID}", as.GetUser).Methods(http.MethodGet)
	as.Router.HandleFunc("/_matrix/app/v1/transactions/{txnID}", as.PutTransaction).Methods(http.MethodPut)
	as.Router.HandleFunc("/_matrix/app/v1/rooms/{roomAlias}", as.GetRoom).Methods(http.MethodGet)
	as.Router.HandleFunc("/_matrix/app/v1/users/{userID}", as.GetUser).Methods(http.MethodGet)
	as.Router.HandleFunc("/_matrix/app/v1/ping", as.PostPing).Methods(http.MethodPost)
	as.Router.HandleFunc("/_matrix/app/unstable/fi.mau.msc2659/ping", as.PostPing).Methods(http.MethodPost)
	as.Router.HandleFunc("/_matrix/mau/live", as.GetLive).Methods(http.MethodGet)
	as.Router.HandleFunc("/_matrix/mau/ready", as.GetReady).Methods(http.MethodGet)

	if len(as.UserAgent) == 0 {
		as.UserAgent = mautrix.DefaultUserAgent
	}
	if len(as.ProcessID) == 0 {
		as.ProcessID = getDefaultProcessID()
	}

	if as.LogConfig != nil {
		log, err := as.LogConfig.Compile()
		if err != nil {
			return false, fmt.Errorf("error initializing logger: %w", err)
		}
		as.Log = *log
		as.Log.Debug().Msg("Logger initialized successfully")
	}

	if len(as.RegistrationPath) > 0 {
		var err error
		as.Registration, err = LoadRegistration(as.RegistrationPath)
		if err != nil {
			return false, err
		}
	}

	if as.LogConfig != nil {
		as.Log.Debug().Msg("Appservice initialized successfully")
	}
	return true, nil
}
