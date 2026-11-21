package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Filesystem FileConfig  `toml:"filesystem"`
	Photos     PhotoConfig `toml:"google_photos"`
	dir        string
}

type FileConfig struct {
	MountPoint string `toml:"mount_point"`
	Root       string `toml:"root"`
	ReadOnly   bool   `toml:"read_only"`
}

type PhotoConfig struct {
	AuthData        string `toml:"auth_data"`
	CacheDBPath     string `toml:"cache_db_path"`
	DeviceModel     string `toml:"device_model"`
	DeviceMake      string `toml:"device_make"`
	UnlimitedUpload bool   `toml:"unlimited_upload"`
	ListMode        string `toml:"list_mode"`
	APKVersion      int64  `toml:"apk_version"`
	AndroidAPI      int64  `toml:"android_api"`
	DownloadCache   bool   `toml:"download_cache"`
	Encoding        string `toml:"encoding"`
}

func (p *PhotoConfig) defaults() {
	if p.DeviceModel == "" {
		p.DeviceModel = "Pixel 9a"
	}
	if p.DeviceMake == "" {
		p.DeviceMake = "Google"
	}
	if p.APKVersion == 0 {
		p.APKVersion = 49029607
	}
	if p.AndroidAPI == 0 {
		p.AndroidAPI = 35
	}
	if p.ListMode == "" {
		p.ListMode = "flat"
	}
	if p.Encoding == "" {
		p.Encoding = "Slash,Dot,CrLf,InvalidUtf8"
	}
}

func configPath() (string, error) {
	if override := os.Getenv("GPHOTOSFS_CONFIG"); override != "" {
		return filepath.Abs(override)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "GooglePhotosFS", "googlephotosfs.toml"), nil
}

func loadConfig() (Config, error) {
	cfg := Config{Filesystem: FileConfig{MountPoint: "P:"}}
	filename, err := configPath()
	if err != nil {
		return cfg, err
	}
	cfg.dir = filepath.Dir(filename)
	raw, err := os.ReadFile(filename)
	if err == nil {
		if err = toml.Unmarshal(raw, &cfg); err != nil {
			return cfg, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	cfg.Photos.defaults()
	if cfg.Filesystem.MountPoint == "" {
		cfg.Filesystem.MountPoint = "P:"
	}
	cfg.Filesystem.Root = strings.Trim(cleanVirtual(cfg.Filesystem.Root), "/")

	return cfg, nil
}

func (cfg Config) save() error {
	filename, err := configPath()
	if err != nil {
		return err
	}
	raw, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	return replaceContents(filename, ".config-", raw)
}

func replaceContents(filename, pattern string, raw []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(filename), pattern)
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(raw)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return os.Rename(temp.Name(), filename)
}

func (cfg Config) runtime() Config {
	if auth := os.Getenv("GP_AUTH_DATA"); auth != "" {
		cfg.Photos.AuthData = auth
	}
	if cfg.Photos.CacheDBPath == "" {
		data, _ := url.ParseQuery(cfg.Photos.AuthData)
		account := strings.ToLower(data.Get("Email"))
		if account == "" {
			account = data.Get("androidId")
		}
		id := sha256.Sum256([]byte(account))
		cfg.Photos.CacheDBPath = filepath.Join(cfg.dir, "library-"+hex.EncodeToString(id[:8])+".db")
	} else if !filepath.IsAbs(cfg.Photos.CacheDBPath) {
		cfg.Photos.CacheDBPath = filepath.Join(cfg.dir, cfg.Photos.CacheDBPath)
	}
	return cfg
}

func driveLetter(arg string) (string, error) {
	letter := strings.ToUpper(arg)
	if len(letter) != 2 || letter[1] != ':' || letter[0] < 'A' || letter[0] > 'Z' {
		return "", fmt.Errorf("letra de unidad inválida: %q", arg)
	}
	return letter, nil
}
