package aliyun

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const journalHead = "fleeting-operation"
const journalChunks = 12

// Journal is a durable, non-secret, single-writer operation record on the group.
type Journal struct {
	Version      int       `json:"v"`
	Kind         string    `json:"k"`
	Token        string    `json:"t"`
	Before       int       `json:"b"`
	Target       int       `json:"d"`
	Count        int       `json:"n,omitempty"`
	IDs          []string  `json:"i,omitempty"`
	Activity     string    `json:"a,omitempty"`
	AcceptedMask uint32    `json:"m,omitempty"`
	CreatedAt    time.Time `json:"ts"`
}

func ReadJournal(g Group) (*Journal, error) {
	head := g.Tag(journalHead)
	if head == "" {
		return nil, nil
	}
	parts := strings.Split(head, ":")
	if len(parts) != 2 {
		return nil, errors.New("invalid operation journal header")
	}
	n, e := strconv.Atoi(parts[0])
	if e != nil || n < 1 || n > journalChunks {
		return nil, errors.New("invalid journal length")
	}
	var encoded string
	for i := 0; i < n; i++ {
		encoded += g.Tag(fmt.Sprintf("fleeting-operation-%d", i))
	}
	hash := sha256.Sum256([]byte(encoded))
	if hex.EncodeToString(hash[:]) != parts[1] {
		return nil, errors.New("operation journal is incomplete; refusing mutations")
	}
	b, e := base64.RawURLEncoding.DecodeString(encoded)
	if e != nil {
		return nil, e
	}
	var j Journal
	if e = json.Unmarshal(b, &j); e != nil {
		return nil, e
	}
	if err := validateJournal(j, g.MaxSize); err != nil {
		return nil, err
	}

	return &j, nil
}
func validateJournal(j Journal, maxSize int) error {
	if j.Version != 1 || (j.Kind != "increase" && j.Kind != "remove") || j.Token == "" || j.Before < 0 || j.Target < 0 || len(j.IDs) > 20 {
		return errors.New("unsupported operation journal")
	}
	if j.Before > maxSize || j.Target > maxSize || j.CreatedAt.IsZero() || j.CreatedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("journal capacity or timestamp is invalid for this group")
	}
	seen := map[string]bool{}
	for _, id := range j.IDs {
		if id == "" || seen[id] {
			return errors.New("invalid journal instance IDs")
		}
		seen[id] = true
	}
	switch j.Kind {
	case "increase":
		if j.Count <= 0 || j.Target-j.Before != j.Count || len(j.IDs) != 0 || j.Activity != "" || j.AcceptedMask != 0 {
			return errors.New("invalid increase journal invariants")
		}
	case "remove":
		if len(j.IDs) == 0 || j.Count != 0 || j.AcceptedMask>>len(j.IDs) != 0 {
			return errors.New("invalid removal journal subset")
		}
		if j.Activity == "" {
			if j.Target != j.Before || j.AcceptedMask != 0 {
				return errors.New("invalid prepared removal journal")
			}
		} else {
			accepted := 0
			for mask := j.AcceptedMask; mask != 0; mask >>= 1 {
				accepted += int(mask & 1)
			}
			if j.Target != j.Before-accepted {
				return errors.New("invalid accepted removal capacity")
			}
		}

	}

	return nil
}

func sameJournalIntent(old, next Journal) bool {
	if old.Kind != next.Kind || old.Token != next.Token || old.Before != next.Before || old.Count != next.Count || !old.CreatedAt.Equal(next.CreatedAt) || !slices.Equal(old.IDs, next.IDs) {
		return false
	}
	if old.Kind == "increase" || old.Activity != "" {
		return old.Target == next.Target && old.Activity == next.Activity && old.AcceptedMask == next.AcceptedMask
	}
	return true // A prepared removal may record its accepted subset and activity.
}

func (c *Client) StoreJournal(ctx context.Context, j Journal) error {
	j.Version = 1
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	n := (len(encoded) + 127) / 128
	if n > journalChunks {
		return errors.New("operation journal exceeds reserved tag capacity")
	}
	g, err := c.Group(ctx)
	if err != nil {
		return err
	}
	if err = validateJournal(j, g.MaxSize); err != nil {
		return err
	}
	old, err := ReadJournal(g)
	if err != nil {
		return err
	}
	if old != nil && !sameJournalIntent(*old, j) {
		return errors.New("journal update changes the logical operation identity")
	}
	other := 0
	for _, t := range g.Tags.Tag {
		if !strings.HasPrefix(t.TagKey, "fleeting-operation") {
			other++
		}
	}
	if other+n+1 > 20 {
		return errors.New("group has insufficient tag capacity for operation journal")
	}
	hash := sha256.Sum256([]byte(encoded))
	head := fmt.Sprintf("%d:%s", n, hex.EncodeToString(hash[:]))
	q := map[string]string{"RegionId": c.Region, "ResourceType": "scalinggroup", "ResourceId.1": c.GroupID, "Tag.1.Key": journalHead, "Tag.1.Value": head, "Tag.1.Propagate": "false"}
	for i := 0; i < n; i++ {
		q[fmt.Sprintf("Tag.%d.Key", i+2)] = fmt.Sprintf("fleeting-operation-%d", i)
		q[fmt.Sprintf("Tag.%d.Value", i+2)] = encoded[i*128 : min((i+1)*128, len(encoded))]
		q[fmt.Sprintf("Tag.%d.Propagate", i+2)] = "false"
	}
	var out struct{ RequestId string }
	if err = c.API.Call(ctx, "ess", "TagResources", q, &out); err != nil {
		return err
	}
	for {
		g, err = c.Group(ctx)
		if err != nil {
			return err
		}
		loaded, e := ReadJournal(g)
		if e == nil && g.Tag(journalHead) == head && loaded != nil && loaded.Token == j.Token {
			return nil
		}
		if err = Wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
func (c *Client) ClearJournal(ctx context.Context) error {
	q := map[string]string{"RegionId": c.Region, "ResourceType": "scalinggroup", "ResourceId.1": c.GroupID, "TagKey.1": journalHead}
	for i := 0; i < journalChunks; i++ {
		q[fmt.Sprintf("TagKey.%d", i+2)] = fmt.Sprintf("fleeting-operation-%d", i)
	}
	var out struct{ RequestId string }
	if err := c.API.Call(ctx, "ess", "UntagResources", q, &out); err != nil {
		return err
	}
	for {
		g, err := c.Group(ctx)
		if err != nil {
			return err
		}
		if g.Tag(journalHead) == "" {
			return nil
		}
		if err = Wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}
