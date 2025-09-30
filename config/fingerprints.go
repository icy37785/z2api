package config

import (
	"encoding/json"
	"math/rand"
	"os"
	"sync"
	"time"
)

// Fingerprint defines the structure for a single browser fingerprint.
type Fingerprint struct {
	XFeVersion       string `json:"x_fe_version"`
	UserAgent        string `json:"user_agent"`
	SecChUa          string `json:"sec_ch_ua"`
	SecChUaMobile    string `json:"sec_ch_ua_mobile"`
	SecChUaPlatform  string `json:"sec_ch_ua_platform"`
}

// FingerprintsData holds all the loaded fingerprint data.
type FingerprintsData struct {
	Fingerprints []Fingerprint `json:"fingerprints"`
	rng          *rand.Rand
	mutex        sync.Mutex
}

var fingerprintsData *FingerprintsData

// LoadFingerprints loads and parses the fingerprints.json file.
func LoadFingerprints(path string) error {
	file, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var data FingerprintsData
	if err := json.Unmarshal(file, &data); err != nil {
		return err
	}

	// Initialize the random number generator
	data.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	fingerprintsData = &data
	return nil
}

// GetRandomFingerprint returns a random fingerprint from the loaded pool.
func GetRandomFingerprint() (Fingerprint, bool) {
	if fingerprintsData == nil || len(fingerprintsData.Fingerprints) == 0 {
		return Fingerprint{}, false
	}

	fingerprintsData.mutex.Lock()
	defer fingerprintsData.mutex.Unlock()

	index := fingerprintsData.rng.Intn(len(fingerprintsData.Fingerprints))
	return fingerprintsData.Fingerprints[index], true
}
