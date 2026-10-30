package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type packageUpgrade struct {
	Version                string `json:"version"`
	PackageSHA256          string `json:"package_sha256"`
	CandidatePackageSHA256 string `json:"candidate_package_sha256"`
	StatePreserved         bool   `json:"state_preserved"`
	RollbackVerified       bool   `json:"rollback_verified"`
}

func managerExample(installed, candidate string, upgrade bool) ([]byte, error) {
	path := installed
	if upgrade {
		// Supply a candidate-compatible configuration to the published baseline.
		// Its exact template is checked against the finally installed package.
		path = candidate
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("acceptance package example is missing")
	}
	return raw, nil
}

func verifyInstalledManagerExample(path string, expected []byte) error {
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("installed candidate example differs from the supplied upgrade configuration template")
	}
	return nil
}

func upgradeBaseline() (version, packageHash, binaryHash string, err error) {
	var pin struct {
		Version       string
		Architectures map[string]struct {
			PackageSHA256 string `json:"package_sha256"`
			BinarySHA256  string `json:"binary_sha256"`
		}
	}
	raw, err := os.ReadFile("tests/fixtures/debian-upgrade.json")
	if err != nil || json.Unmarshal(raw, &pin) != nil {
		return "", "", "", errors.New("cannot read the reviewed previous-release pin")
	}
	row := pin.Architectures["amd64"]
	validHash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if pin.Version == "" || !validHash.MatchString(row.PackageSHA256) || !validHash.MatchString(row.BinarySHA256) {
		return "", "", "", errors.New("previous-release pin lacks exact package and binary hashes")
	}
	return pin.Version, row.PackageSHA256, row.BinarySHA256, nil
}

func installedUpgradeBaseline() ([]byte, error) {
	_, _, expected, err := upgradeBaseline()
	if err != nil {
		return nil, err
	}
	installed, err := os.ReadFile(packagedPlugin)
	hash := sha256.Sum256(installed)
	if err != nil || hex.EncodeToString(hash[:]) != expected {
		return nil, errors.New("installed Manager does not match the pinned previous release")
	}
	return installed, nil
}

func (m *packagedManager) upgrade(ctx context.Context, candidate, candidateHash, previous, version string, expected []byte) (*packageUpgrade, error) {
	if err := m.owned(ctx); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(candidate) || !filepath.IsAbs(previous) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(candidateHash) {
		return nil, errors.New("package transition requires absolute sealed artifact paths and checksum")
	}
	helper, err := filepath.Abs("tools/debian_upgrade.py")
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(expected)
	raw, err := boundedServiceCommand(ctx, 4*time.Minute, "python3", helper, "--disposable-host",
		"--candidate", candidate, "--candidate-sha", candidateHash, "--candidate-binary-sha", hex.EncodeToString(hash[:]),
		"--version", version, "--previous", previous)
	if err != nil {
		return nil, err
	}
	var proof packageUpgrade
	baselineVersion, baselineHash, _, pinErr := upgradeBaseline()
	if json.Unmarshal(raw, &proof) != nil || pinErr != nil || !proof.StatePreserved || !proof.RollbackVerified ||
		proof.Version != baselineVersion || proof.PackageSHA256 != baselineHash || proof.CandidatePackageSHA256 != candidateHash {
		return nil, errors.New("package transition lacks bound state and rollback proof")
	}
	installed, err := os.ReadFile(packagedPlugin)
	if err != nil || !bytes.Equal(installed, expected) {
		return nil, errors.New("upgraded package differs from the sealed candidate")
	}
	if err := m.owned(ctx); err != nil {
		return nil, err
	}
	return &proof, nil
}
