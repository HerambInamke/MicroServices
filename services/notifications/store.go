package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNotFound = errors.New("not found")

const (
	maxPerUser = 200
	keepFor    = 30 * 24 * time.Hour
	dedupFor   = 7 * 24 * time.Hour
)

type Notification struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	RefType   string `json:"refType"`
	RefID     string `json:"refId"`
	Read      bool   `json:"read"`
	CreatedAt string `json:"createdAt"`
}

type Store interface {
	// Add stores n unless eventID was already seen; reports whether it was stored.
	Add(ctx context.Context, n Notification, eventID string) (bool, error)
	List(ctx context.Context, userID string, unreadOnly bool) ([]Notification, error)
	MarkRead(ctx context.Context, userID, id string) error
	// Subscribe streams the user's new notifications as JSON until stop is called.
	Subscribe(ctx context.Context, userID string) (msgs <-chan string, stop func(), err error)
}

type redisStore struct{ rdb *redis.Client }

func newRedisStore(ctx context.Context, url string) (*redisStore, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, err
	}
	return &redisStore{rdb: rdb}, nil
}

// One script so the dedup marker and the notification land together: a crash between
// them would otherwise mark the event seen and lose the notification on redelivery.
// KEYS: seen, notif, list. ARGV: dedupSecs, keepSecs, max, id, channel, json, then hash field/value pairs.
var addScript = redis.NewScript(`
if not redis.call('SET', KEYS[1], '1', 'NX', 'EX', ARGV[1]) then return 0 end
redis.call('HSET', KEYS[2], unpack(ARGV, 7))
redis.call('EXPIRE', KEYS[2], ARGV[2])
redis.call('LPUSH', KEYS[3], ARGV[4])
redis.call('LTRIM', KEYS[3], 0, tonumber(ARGV[3]) - 1)
redis.call('EXPIRE', KEYS[3], ARGV[2])
redis.call('PUBLISH', ARGV[5], ARGV[6])
return 1
`)

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

func (s *redisStore) Add(ctx context.Context, n Notification, eventID string) (bool, error) {
	keys := []string{"seen:" + eventID, "notif:" + n.ID, "notifs:" + n.UserID}
	live, _ := json.Marshal(n)
	args := []any{int(dedupFor.Seconds()), int(keepFor.Seconds()), maxPerUser, n.ID, channel(n.UserID), string(live),
		"userId", n.UserID, "type", n.Type, "title", n.Title, "body", n.Body,
		"refType", n.RefType, "refId", n.RefID, "read", "0", "createdAt", n.CreatedAt}
	added, err := addScript.Run(ctx, s.rdb, keys, args...).Int()
	return added == 1, err
}

func (s *redisStore) List(ctx context.Context, userID string, unreadOnly bool) ([]Notification, error) {
	ids, err := s.rdb.LRange(ctx, "notifs:"+userID, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, "notif:"+id)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}
	out := []Notification{}
	for i, c := range cmds {
		h := c.Val()
		if len(h) == 0 { // expired before the list entry was trimmed
			continue
		}
		n := Notification{ID: ids[i], UserID: h["userId"], Type: h["type"], Title: h["title"], Body: h["body"],
			RefType: h["refType"], RefID: h["refId"], Read: h["read"] == "1", CreatedAt: h["createdAt"]}
		if unreadOnly && n.Read {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// MarkRead reports ErrNotFound for someone else's notification, so ids cannot be probed.
func (s *redisStore) MarkRead(ctx context.Context, userID, id string) error {
	owner, err := s.rdb.HGet(ctx, "notif:"+id, "userId").Result()
	if err == redis.Nil || (err == nil && owner != userID) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.rdb.HSet(ctx, "notif:"+id, "read", "1").Err()
}

func channel(userID string) string { return "notify:" + userID }

func (s *redisStore) Subscribe(ctx context.Context, userID string) (<-chan string, func(), error) {
	ps := s.rdb.Subscribe(ctx, channel(userID))
	// Wait for the subscription to be confirmed, so nothing published after we return is missed.
	if _, err := ps.Receive(ctx); err != nil {
		ps.Close()
		return nil, nil, err
	}
	out := make(chan string)
	go func() {
		defer close(out)
		for m := range ps.Channel() {
			select {
			case out <- m.Payload:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { ps.Close() }, nil
}
