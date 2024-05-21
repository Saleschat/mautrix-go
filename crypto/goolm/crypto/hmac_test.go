package crypto_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"testing"

	"maunium.net/go/mautrix/crypto/goolm/crypto"
)

func TestHMACSha256(t *testing.T) {
	key := []byte("test key")
	message := []byte("test message")
	hash := crypto.HMACSHA256(key, message)
	if !bytes.Equal(hash, crypto.HMACSHA256(key, message)) {
		t.Fail()
	}
	str := "A4M0ovdiWHaZ5msdDFbrvtChFwZIoIaRSVGmv8bmPtc"
	result, err := base64.RawStdEncoding.DecodeString(str)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, hash) {
		t.Fail()
	}
}

func TestHKDFSha256(t *testing.T) {
	message := []byte("test content")
	hkdf := crypto.HKDFSHA256(message, nil, nil)
	hkdf2 := crypto.HKDFSHA256(message, nil, nil)
	result := make([]byte, 32)
	if _, err := io.ReadFull(hkdf, result); err != nil {
		t.Fatal(err)
	}
	result2 := make([]byte, 32)
	if _, err := io.ReadFull(hkdf2, result2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, result2) {
		t.Fail()
	}
}

func TestSha256Case1(t *testing.T) {
	input := make([]byte, 0)
	expected := []byte{
		0xE3, 0xB0, 0xC4, 0x42, 0x98, 0xFC, 0x1C, 0x14,
		0x9A, 0xFB, 0xF4, 0xC8, 0x99, 0x6F, 0xB9, 0x24,
		0x27, 0xAE, 0x41, 0xE4, 0x64, 0x9B, 0x93, 0x4C,
		0xA4, 0x95, 0x99, 0x1B, 0x78, 0x52, 0xB8, 0x55,
	}
	result := crypto.SHA256(input)
	if !bytes.Equal(expected, result) {
		t.Fatalf("result not as expected:\n%v\n%v\n", result, expected)
	}
}

func TestHMACCase1(t *testing.T) {
	input := make([]byte, 0)
	expected := []byte{
		0xb6, 0x13, 0x67, 0x9a, 0x08, 0x14, 0xd9, 0xec,
		0x77, 0x2f, 0x95, 0xd7, 0x78, 0xc3, 0x5f, 0xc5,
		0xff, 0x16, 0x97, 0xc4, 0x93, 0x71, 0x56, 0x53,
		0xc6, 0xc7, 0x12, 0x14, 0x42, 0x92, 0xc5, 0xad,
	}
	result := crypto.HMACSHA256(input, input)
	if !bytes.Equal(expected, result) {
		t.Fatalf("result not as expected:\n%v\n%v\n", result, expected)
	}
}

func TestHDKFCase1(t *testing.T) {
	input := []byte{
		0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b,
		0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b,
		0x0b, 0x0b, 0x0b, 0x0b, 0x0b, 0x0b,
	}
	salt := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c,
	}
	info := []byte{
		0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7,
		0xf8, 0xf9,
	}
	expectedHMAC := []byte{
		0x07, 0x77, 0x09, 0x36, 0x2c, 0x2e, 0x32, 0xdf,
		0x0d, 0xdc, 0x3f, 0x0d, 0xc4, 0x7b, 0xba, 0x63,
		0x90, 0xb6, 0xc7, 0x3b, 0xb5, 0x0f, 0x9c, 0x31,
		0x22, 0xec, 0x84, 0x4a, 0xd7, 0xc2, 0xb3, 0xe5,
	}
	result := crypto.HMACSHA256(salt, input)
	if !bytes.Equal(expectedHMAC, result) {
		t.Fatalf("result not as expected:\n%v\n%v\n", result, expectedHMAC)
	}
	expectedHDKF := []byte{
		0x3c, 0xb2, 0x5f, 0x25, 0xfa, 0xac, 0xd5, 0x7a,
		0x90, 0x43, 0x4f, 0x64, 0xd0, 0x36, 0x2f, 0x2a,
		0x2d, 0x2d, 0x0a, 0x90, 0xcf, 0x1a, 0x5a, 0x4c,
		0x5d, 0xb0, 0x2d, 0x56, 0xec, 0xc4, 0xc5, 0xbf,
		0x34, 0x00, 0x72, 0x08, 0xd5, 0xb8, 0x87, 0x18,
		0x58, 0x65,
	}
	resultReader := crypto.HKDFSHA256(input, salt, info)
	result = make([]byte, len(expectedHDKF))
	if _, err := io.ReadFull(resultReader, result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedHDKF, result) {
		t.Fatalf("result not as expected:\n%v\n%v\n", result, expectedHDKF)
	}
}
