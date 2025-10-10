package config

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"z2api/utils"
)

// Metadata contains descriptive info about the fingerprints file.
type Metadata struct {
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
}

// FingerprintMetadata holds the core browser and OS information for a specific fingerprint.
type FingerprintMetadata struct {
	Browser  string `json:"browser"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Platform string `json:"platform"`
}

// HeaderTemplates defines different sets of headers for various request scenarios.
type HeaderTemplates struct {
	HTML map[string]string `json:"html"`
	XHR  map[string]string `json:"xhr"`
	JS   map[string]string `json:"js"`
}

// Fingerprint defines the structure for a single, consistent browser fingerprint.
type Fingerprint struct {
	ID        string              `json:"id"`
	Metadata  FingerprintMetadata `json:"metadata"`
	UserAgent string              `json:"user_agent"`
	Headers   HeaderTemplates     `json:"headers"`
}

// FingerprintsData holds all the loaded fingerprint data and session state.
type FingerprintsData struct {
	Metadata       Metadata               `json:"metadata"`
	Fingerprints   []Fingerprint          `json:"fingerprints"`
	fingerprintMap map[string]Fingerprint `json:"-"` // For quick lookup by ID
	sessionStore   map[string]string      `json:"-"` // In-memory session store <session_id, fingerprint_id>
	rng            *rand.Rand
	mutex          sync.RWMutex
}

var (
	fingerprintsData *FingerprintsData
	once             sync.Once
)

// LoadFingerprints loads, validates, and initializes the fingerprint data from a given path.
// It ensures that this process is only executed once.
func LoadFingerprints(path string) error {
	var err error
	once.Do(func() {
		file, readErr := os.ReadFile(path)
		if readErr != nil {
			err = fmt.Errorf("failed to read fingerprints file: %w", readErr)
			return
		}

		var data FingerprintsData
		if unmarshalErr := sonic.Unmarshal(file, &data); unmarshalErr != nil {
			err = fmt.Errorf("failed to parse fingerprints JSON: %w", unmarshalErr)
			return
		}

		// Initialize maps and random number generator
		data.fingerprintMap = make(map[string]Fingerprint)
		data.sessionStore = make(map[string]string)
		// Seed with a more reliable source of entropy
		data.rng = rand.New(rand.NewSource(time.Now().UnixNano()))

		// Validate and populate the fingerprint map
		var validFingerprints []Fingerprint
		for _, fp := range data.Fingerprints {
			if fp.ID == "" {
				utils.LogWarn("Skipping fingerprint with empty ID")
				continue
			}
			if _, exists := data.fingerprintMap[fp.ID]; exists {
				utils.LogWarn("Duplicate fingerprint ID found, skipping", "id", fp.ID)
				continue
			}
			data.fingerprintMap[fp.ID] = fp
			validFingerprints = append(validFingerprints, fp)
		}
		data.Fingerprints = validFingerprints

		if len(data.Fingerprints) == 0 {
			err = fmt.Errorf("no valid fingerprints were loaded from %s", path)
			return
		}

		fingerprintsData = &data
		utils.LogInfo("Successfully loaded fingerprints", "count", len(fingerprintsData.Fingerprints), "version", fingerprintsData.Metadata.Version)
	})
	return err
}

// GetFingerprintByID returns a fingerprint by its unique ID.
// It is safe for concurrent use.
func GetFingerprintByID(id string) (*Fingerprint, bool) {
	if fingerprintsData == nil || fingerprintsData.fingerprintMap == nil {
		return nil, false
	}

	fingerprintsData.mutex.RLock()
	defer fingerprintsData.mutex.RUnlock()

	fp, ok := fingerprintsData.fingerprintMap[id]
	if !ok {
		return nil, false
	}
	// Return a copy to prevent modification of the original map entry
	return &fp, true
}

// GetFingerprintForSession manages and returns a consistent fingerprint for a given session ID.
// If a session ID is new, it assigns a random fingerprint and stores the association.
// Subsequent calls with the same session ID will return the same fingerprint.
// It is safe for concurrent use.
func GetFingerprintForSession(sessionID string) (*Fingerprint, bool) {
	if fingerprintsData == nil || len(fingerprintsData.Fingerprints) == 0 {
		return nil, false
	}

	// First, use a read lock to check for an existing session
	fingerprintsData.mutex.RLock()
	fpID, sessionExists := fingerprintsData.sessionStore[sessionID]
	fingerprintsData.mutex.RUnlock()

	if !sessionExists {
		// If the session doesn't exist, we need a full lock to create it
		fingerprintsData.mutex.Lock()
		// Double-check in case another goroutine created it while we were waiting for the lock
		fpID, sessionExists = fingerprintsData.sessionStore[sessionID]
		if !sessionExists {
			// Assign a new random one
			index := fingerprintsData.rng.Intn(len(fingerprintsData.Fingerprints))
			fp := fingerprintsData.Fingerprints[index]
			fingerprintsData.sessionStore[sessionID] = fp.ID
			fpID = fp.ID
		}
		fingerprintsData.mutex.Unlock()
	}

	// At this point, fpID is guaranteed to be set, so we can use the (already locked) GetFingerprintByID
	return GetFingerprintByID(fpID)
}
