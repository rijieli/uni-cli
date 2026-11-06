package main

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const mcpConfigKeychainService = "uni-cli MCP configuration"

type configKeychain interface {
	Read(service string) ([]byte, bool, error)
	Write(service string, data []byte) error
}

type macConfigKeychain struct{}

func (macConfigKeychain) Read(service string) ([]byte, bool, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", mcpConfigKeychainService, "-a", service, "-w").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s configuration from Keychain: %w", service, err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, false, fmt.Errorf("invalid %s configuration in Keychain", service)
	}
	return data, true, nil
}

func (k macConfigKeychain) Write(service string, data []byte) error {
	if service != "linear" && service != "notion" {
		return errors.New("unsupported Keychain service")
	}
	value := base64.StdEncoding.EncodeToString(data)
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s \"" + mcpConfigKeychainService + "\" -a " + service + " -w " + value + "\n")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("save %s configuration in Keychain: %w", service, err)
	}
	stored, found, err := k.Read(service)
	if err != nil || !found || !bytes.Equal(stored, data) {
		return fmt.Errorf("could not verify %s configuration in Keychain", service)
	}
	return nil
}

func loadKeychainConfig(path, svc string, keychain configKeychain) (*config, error) {
	stored, found, err := keychain.Read(svc)
	if err != nil {
		return nil, err
	}
	legacy, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if readErr == nil {
		var cfg config
		if err := json.Unmarshal(legacy, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if found && !sameConfigJSON(stored, legacy) {
			return nil, fmt.Errorf("%s: legacy file and Keychain configuration differ; resolve the conflict before migrating", path)
		}
		if !found {
			if err := keychain.Write(svc, legacy); err != nil {
				return nil, fmt.Errorf("migrate %s: %w", svc, err)
			}
			stored, found, err = keychain.Read(svc)
			if err != nil || !found || !sameConfigJSON(stored, legacy) {
				return nil, fmt.Errorf("could not verify migrated %s configuration in Keychain; %s was not removed", svc, path)
			}
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove migrated configuration %s: %w", path, err)
		}
	}
	cfg := &config{}
	if found {
		if err := json.Unmarshal(stored, cfg); err != nil {
			return nil, fmt.Errorf("invalid %s configuration in Keychain: %w", svc, err)
		}
	}
	cfg.Endpoint = cmp.Or(cfg.Endpoint, presets[svc].endpoint)
	if cfg.Endpoint == "" {
		return nil, errors.New("missing endpoint")
	}
	cfg.storedAPIKey = cfg.APIKey
	if envName := presets[svc].apiKeyEnv; envName != "" {
		if value := os.Getenv(envName); value != "" {
			cfg.APIKey = value
			cfg.envAPIKey = true
		}
	}
	return cfg, nil
}

func saveKeychainConfig(svc string, cfg *config, keychain configKeychain) error {
	copy := *cfg
	if copy.envAPIKey {
		copy.APIKey = copy.storedAPIKey
	}
	data, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	return keychain.Write(svc, data)
}

func sameConfigJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	x, err := json.Marshal(left)
	if err != nil {
		return false
	}
	y, err := json.Marshal(right)
	return err == nil && bytes.Equal(x, y)
}
