package mas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// CartFile stores the guest cart id between invocations: one file, one cart id.
// Tests inject a Path under t.TempDir(), so nothing writes to $HOME.
type CartFile struct{ Path string }

// DefaultCartPath is <user config dir>/multisuper/mas-cart.json.
func DefaultCartPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the user config dir: %w", err)
	}
	return filepath.Join(dir, "multisuper", "mas-cart.json"), nil
}

// Load returns the stored cart id, or ErrNoCart when there is none.
func (f CartFile) Load() (string, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoCart
	}
	if err != nil {
		return "", fmt.Errorf("reading the cart file: %w", err)
	}
	var doc struct {
		CartID string `json:"cartId"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("invalid cart file %s: %w", f.Path, err)
	}
	if doc.CartID == "" {
		return "", ErrNoCart
	}
	return doc.CartID, nil
}

// Save writes the cart id with owner-only permissions.
func (f CartFile) Save(cartID string) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return fmt.Errorf("creating the config dir: %w", err)
	}
	raw, err := json.Marshal(struct {
		CartID string `json:"cartId"`
	}{cartID})
	if err != nil {
		return fmt.Errorf("encoding the cart file: %w", err)
	}
	if err := os.WriteFile(f.Path, raw, 0o600); err != nil {
		return fmt.Errorf("writing the cart file: %w", err)
	}
	// WriteFile keeps the mode of an existing file, so force 0600 either way.
	if err := os.Chmod(f.Path, 0o600); err != nil {
		return fmt.Errorf("securing the cart file: %w", err)
	}
	return nil
}

// Forget removes the stored cart id. A missing file is not an error.
func (f CartFile) Forget() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the cart file: %w", err)
	}
	return nil
}
