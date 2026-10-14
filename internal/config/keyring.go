package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	serviceName = "azdo-tui"
	userName    = "pat" // legacy single-PAT key; also the fallback for every org
)

// patUserForOrg returns the keyring user name for an organisation-scoped PAT.
// Empty org falls back to the legacy userName so SetPAT/GetPAT stay consistent.
func patUserForOrg(org string) string {
	org = strings.TrimSpace(org)
	if org == "" {
		return userName
	}
	return "pat:" + org
}

// orgPATEnvName builds the per-organisation environment-variable name
// AZDO_PAT_<ORG> where non-alphanumeric characters in the organisation name
// become underscores and letters are upper-cased.
func orgPATEnvName(org string) string {
	var b strings.Builder
	b.WriteString("AZDO_PAT_")
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ErrNotFound is returned when a PAT is not found in the keyring
var ErrNotFound = errors.New("PAT not found in keyring")

// keyringProvider defines the interface for keyring operations
// This allows for mocking in tests
type keyringProvider interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// systemKeyring is a wrapper around the go-keyring library
type systemKeyring struct{}

func (s *systemKeyring) Get(service, user string) (string, error) {
	secret, err := keyring.Get(service, user)
	if err != nil {
		if err == keyring.ErrNotFound {
			return "", ErrNotFound
		}
		return "", err
	}
	return secret, nil
}

func (s *systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (s *systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// KeyringStore manages PAT storage in the system keyring
type KeyringStore struct {
	provider keyringProvider
}

// NewKeyringStore creates a new KeyringStore with the system keyring
func NewKeyringStore() *KeyringStore {
	return &KeyringStore{
		provider: &systemKeyring{},
	}
}

// SetPAT stores a Personal Access Token in the system keyring under the
// legacy shared user name. Prefer SetPATForOrg when configuring a
// multi-organisation setup.
func (k *KeyringStore) SetPAT(token string) error {
	return k.SetPATForOrg("", token)
}

// SetPATForOrg stores a Personal Access Token for the given Azure DevOps
// organisation. An empty org writes the legacy shared entry (same as SetPAT).
func (k *KeyringStore) SetPATForOrg(org, token string) error {
	if token == "" {
		return errors.New("token cannot be empty")
	}

	if err := k.provider.Set(serviceName, patUserForOrg(org), token); err != nil {
		return fmt.Errorf("failed to store PAT in keyring: %w", err)
	}

	return nil
}

// GetPAT retrieves the shared (legacy) Personal Access Token from the system
// keyring. If the keyring is unavailable, it falls back to the AZDO_PAT
// environment variable. Prefer GetPATForOrg when resolving a token for a
// specific organisation.
func (k *KeyringStore) GetPAT() (string, error) {
	token, err := k.provider.Get(serviceName, userName)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrNotFound
		}
		// Keyring access failed - try environment variable fallback
		if envPAT := os.Getenv("AZDO_PAT"); envPAT != "" {
			return envPAT, nil
		}
		return "", fmt.Errorf("failed to retrieve PAT from keyring and AZDO_PAT environment variable not set: %w. "+
			"Please either fix your system keyring or set the AZDO_PAT environment variable", err)
	}

	return token, nil
}

// GetPATForOrg resolves the Personal Access Token for one organisation.
// Fallback chain, first match wins:
//  1. keyring entry pat:<org>
//  2. legacy keyring entry pat (shared across every organisation)
//  3. environment variable AZDO_PAT_<ORG> (see orgPATEnvName)
//  4. environment variable AZDO_PAT
//
// ErrNotFound is returned only when every step misses. A keyring access
// error (as opposed to a missing entry) still falls through to the
// environment-variable steps before surfacing.
func (k *KeyringStore) GetPATForOrg(org string) (string, error) {
	org = strings.TrimSpace(org)
	if org != "" {
		token, err := k.provider.Get(serviceName, patUserForOrg(org))
		if err == nil && token != "" {
			return token, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			// Keyring broken — keep falling through to env vars below.
		}
	}

	token, err := k.provider.Get(serviceName, userName)
	if err == nil && token != "" {
		return token, nil
	}
	keyringHardErr := err != nil && !errors.Is(err, ErrNotFound)

	if org != "" {
		if envPAT := os.Getenv(orgPATEnvName(org)); envPAT != "" {
			return envPAT, nil
		}
	}
	if envPAT := os.Getenv("AZDO_PAT"); envPAT != "" {
		return envPAT, nil
	}

	if keyringHardErr {
		return "", fmt.Errorf("failed to retrieve PAT from keyring and AZDO_PAT environment variable not set: %w. "+
			"Please either fix your system keyring or set the AZDO_PAT environment variable", err)
	}
	return "", ErrNotFound
}

// DeletePAT removes the legacy shared Personal Access Token from the system keyring.
func (k *KeyringStore) DeletePAT() error {
	return k.DeletePATForOrg("")
}

// DeletePATForOrg removes the Personal Access Token for the given organisation.
// An empty org deletes the legacy shared entry.
func (k *KeyringStore) DeletePATForOrg(org string) error {
	if err := k.provider.Delete(serviceName, patUserForOrg(org)); err != nil {
		return fmt.Errorf("failed to delete PAT from keyring: %w", err)
	}

	return nil
}
