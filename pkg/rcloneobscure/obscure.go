package rcloneobscure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

var obscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// ConfigValue returns the value to write into rclone.conf.
func ConfigValue(key, value string, passObscured bool) (string, error) {
	if key == "pass" && value != "" {
		if passObscured {
			return value, nil
		}
		return ObscurePass(value)
	}
	return value, nil
}

// PrepareConfigForLegacyAgent obscures pass fields before sending config to a
// legacy agent that expects rclone.conf-ready values in policy payloads.
func PrepareConfigForLegacyAgent(config map[string]string) (map[string]string, error) {
	if len(config) == 0 {
		return config, nil
	}
	prepared := make(map[string]string, len(config))
	for key, value := range config {
		next, err := ConfigValue(key, value, false)
		if err != nil {
			return nil, err
		}
		prepared[key] = next
	}
	return prepared, nil
}

// ObscurePass returns an rclone-obscured password for plain text input.
func ObscurePass(value string) (string, error) {
	plaintext := []byte(value)
	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("generate rclone obscure iv: %w", err)
	}
	if err := cryptValue(ciphertext[aes.BlockSize:], plaintext, iv); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// RevealPass decodes an rclone-obscured password.
func RevealPass(value string) (string, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed when revealing password - is it obscured?: %w", err)
	}
	if len(ciphertext) < aes.BlockSize {
		return "", errors.New("input too short when revealing password - is it obscured?")
	}
	iv := ciphertext[:aes.BlockSize]
	buf := ciphertext[aes.BlockSize:]
	if err := cryptValue(buf, buf, iv); err != nil {
		return "", err
	}
	return string(buf), nil
}

func cryptValue(out []byte, in []byte, iv []byte) error {
	block, err := aes.NewCipher(obscureKey)
	if err != nil {
		return fmt.Errorf("create rclone obscure cipher: %w", err)
	}
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(out, in)
	return nil
}
