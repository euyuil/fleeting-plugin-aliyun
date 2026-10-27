package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
)

func EvidenceDirectory(base, runID, attempt string) (string, error) {
	root, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if runID == "" || attempt == "" {
		return filepath.Join(root, "not-run"), nil
	}
	id, e := strconv.ParseUint(runID, 10, 64)
	n, e2 := strconv.ParseUint(attempt, 10, 32)
	if e != nil || e2 != nil || id == 0 || n == 0 {
		return "", errors.New("run ID and attempt must be positive integers")
	}
	return filepath.Join(root, "run-"+runID+"-"+attempt), nil
}
func baselineDigest(ids []string) string {
	canonical := append([]string{}, ids...)
	slices.Sort(canonical)
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
