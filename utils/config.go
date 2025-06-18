package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/PlakarKorp/kloset/config"
	"gopkg.in/yaml.v3"
)

type configHandler struct {
	Path string
}

func newConfigHandler(path string) *configHandler {
	return &configHandler{
		Path: path,
	}
}

func (cl *configHandler) Load() (*config.Config, error) {

	// Load old config if found
	oldpath := filepath.Join(cl.Path, "plakar.yml")
	cfg, err := LoadOldConfigIfExists(oldpath)
	if err != nil {
		return nil, fmt.Errorf("error reading old config file: %w", err)
	}

	if cfg != nil {
		// Save the config in the new format and remove the previous file
		err = SaveConfig(cl.Path, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to update old config file: %w", err)
		}
		os.Remove(oldpath)
		return cfg, nil
	}

	cfg = config.NewConfig()
	err = cl.load("sources.yml", &cfg.Sources)
	if err != nil {
		return nil, err
	}
	err = cl.load("destinations.yml", &cfg.Destinations)
	if err != nil {
		return nil, err
	}
	err = cl.load("klosets.yml", &cfg.Repositories)
	if err != nil {
		return nil, err
	}
	for k, v := range cfg.Repositories {
		if _, ok := v[".isDefault"]; ok {
			cfg.DefaultRepository = k
			delete(v, ".isDefault")
		}
	}

	return cfg, nil
}

func (cl *configHandler) Save(cfg *config.Config) error {
	err := cl.save("sources.yml", cfg.Sources)
	if err != nil {
		return err
	}
	err = cl.save("destinations.yml", cfg.Destinations)
	if err != nil {
		return err
	}
	for k, v := range cfg.Repositories {
		if k == cfg.DefaultRepository {
			v[".isDefault"] = "yes"
		}
	}
	err = cl.save("klosets.yml", cfg.Repositories)
	if err != nil {
		return err
	}
	return nil
}

func (cl *configHandler) load(filename string, dst any) error {
	path := filepath.Join(cl.Path, filename)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("error reading config file: %w", err)
	}
	defer f.Close()

	err = yaml.NewDecoder(f).Decode(dst)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	return nil
}

func (cl *configHandler) save(filename string, src any) error {
	path := filepath.Join(cl.Path, filename)
	tmpFile, err := os.CreateTemp(cl.Path, "config.*.yml")
	if err != nil {
		return err
	}

	err = yaml.NewEncoder(tmpFile).Encode(src)
	tmpFile.Close()

	if err == nil {
		err = os.Rename(tmpFile.Name(), path)
	}

	if err != nil {
		os.Remove(tmpFile.Name())
		return err
	}

	return nil
}

func LoadConfig(configDir string) (*config.Config, error) {
	cl := newConfigHandler(configDir)
	cfg, err := cl.Load()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func SaveConfig(configDir string, cfg *config.Config) error {
	return newConfigHandler(configDir).Save(cfg)
}
