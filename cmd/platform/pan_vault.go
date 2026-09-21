package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var panPattern = regexp.MustCompile(`^[0-9]{16,19}$`)
var cardIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func vaultPath(cardID string) (string, error) {
	if !cardIDPattern.MatchString(cardID) {
		return "", errors.New("неверный ID карты")
	}
	dir := os.Getenv("PAN_VAULT_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("хранилище номеров карт не настроено")
	}
	return filepath.Join(dir, cardID+".pan"), nil
}

func panAEAD() (cipher.AEAD, error) {
	path := os.Getenv("PAN_KEY_FILE")
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("ключ номеров карт не настроен")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0007 != 0 {
		return nil, errors.New("ключ номеров карт недоступен")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("ключ номеров карт недоступен")
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("неверный формат ключа номеров карт")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func validatePAN(pan, mask string) error {
	if !panPattern.MatchString(pan) || !validLuhn(pan) {
		return errors.New("укажите действительный полный номер карты")
	}
	if len(mask) != 16 || pan[:6] != mask[:6] || pan[len(pan)-4:] != mask[12:] {
		return errors.New("номер карты не соответствует её маске")
	}
	return nil
}

func savePAN(cardID, pan string) error {
	path, err := vaultPath(cardID)
	if err != nil {
		return err
	}
	data, err := sealSensitive([]byte(pan), []byte(cardID))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("хранилище номеров карт недоступно")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pan-write-")
	if err != nil {
		return errors.New("не удалось сохранить номер карты")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return errors.New("не удалось сохранить номер карты")
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return errors.New("не удалось сохранить номер карты")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return errors.New("не удалось сохранить номер карты")
	}
	if err = f.Close(); err != nil {
		return errors.New("не удалось сохранить номер карты")
	}
	if err = os.Link(f.Name(), path); os.IsExist(err) {
		return errors.New("номер этой карты уже сохранён")
	} else if err != nil {
		return errors.New("не удалось сохранить номер карты")
	}
	dir, err := os.Open(filepath.Dir(path))
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func loadPAN(cardID string) (string, error) {
	path, err := vaultPath(cardID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", errors.New("у карты нет полного номера")
	}
	if err != nil {
		return "", errors.New("номер карты недоступен")
	}
	plain, err := openSensitive(data, []byte(cardID))
	if err != nil || !panPattern.Match(plain) {
		return "", fmt.Errorf("номер карты недоступен")
	}
	return string(plain), nil
}

func sealSensitive(plain, associated []byte) ([]byte, error) {
	aead, err := panAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	data := append([]byte{1}, nonce...)
	return append(data, aead.Seal(nil, nonce, plain, associated)...), nil
}

func openSensitive(data, associated []byte) ([]byte, error) {
	aead, err := panAEAD()
	if err != nil {
		return nil, err
	}
	if len(data) < 1+aead.NonceSize()+aead.Overhead() || data[0] != 1 {
		return nil, errors.New("неверный формат зашифрованного файла")
	}
	nonce := data[1 : 1+aead.NonceSize()]
	return aead.Open(nil, nonce, data[1+aead.NonceSize():], associated)
}
