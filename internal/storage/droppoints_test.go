package storage

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/domain"
	"github.com/shunichironomura/drop-point/internal/token"
)

// baseTime is a fixed clock at second granularity so timestamps round-trip
// through Unix-second storage exactly.
var baseTime = time.Unix(1_700_000_000, 0).UTC()

// dropPoint is a created drop point plus the raw capability tokens the test used
// to create it, so authorization paths can be exercised.
type dropPoint struct {
	dp        *domain.DropPoint
	rawDrop   string
	rawPickup string
}

type createOpts struct {
	id         string
	apiTokenID string
	createdAt  time.Time
	ttl        time.Duration
}

func defaultCreateOpts() createOpts {
	return createOpts{
		id:         "dp_default",
		apiTokenID: "desktop-main",
		createdAt:  baseTime,
		ttl:        10 * time.Minute,
	}
}

// create inserts a drop point with fresh random tokens, returning the stored
// record alongside the raw tokens.
func create(t *testing.T, s *Store, o createOpts) dropPoint {
	t.Helper()
	rawDrop, err := token.NewDropToken()
	if err != nil {
		t.Fatalf("NewDropToken: %v", err)
	}
	rawPickup, err := token.NewPickupToken()
	if err != nil {
		t.Fatalf("NewPickupToken: %v", err)
	}
	dp, err := s.CreateDropPoint(context.Background(), domain.CreateParams{
		ID:              o.id,
		APITokenID:      o.apiTokenID,
		DropTokenHash:   token.Hash(rawDrop),
		PickupTokenHash: token.Hash(rawPickup),
		MaxBytes:        1024,
		CreatedAt:       o.createdAt,
		ExpiresAt:       o.createdAt.Add(o.ttl),
	})
	if err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	return dropPoint{dp: dp, rawDrop: rawDrop, rawPickup: rawPickup}
}

func TestCreateDropPointAndGet(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	if got := created.dp.Status; got != domain.StatusOpen {
		t.Errorf("created status = %q, want open", got)
	}
	if created.dp.EncryptedSize != nil {
		t.Errorf("created encrypted_size = %v, want nil", *created.dp.EncryptedSize)
	}
	if created.dp.DroppedAt != nil || created.dp.FirstPickedUpAt != nil || created.dp.ClosedAt != nil {
		t.Error("created drop point should have nil dropped_at/first_picked_up_at/closed_at")
	}
	if !created.dp.CreatedAt.Equal(baseTime) {
		t.Errorf("created_at = %v, want %v", created.dp.CreatedAt, baseTime)
	}
	if !created.dp.ExpiresAt.Equal(baseTime.Add(10 * time.Minute)) {
		t.Errorf("expires_at = %v, want %v", created.dp.ExpiresAt, baseTime.Add(10*time.Minute))
	}

	got, err := s.GetDropPoint(ctx, created.dp.ID)
	if err != nil {
		t.Fatalf("GetDropPoint: %v", err)
	}
	if got.ID != created.dp.ID || got.APITokenID != "desktop-main" || got.Status != domain.StatusOpen {
		t.Errorf("round-tripped drop point mismatch: %+v", got)
	}
	if got.DropTokenHash != created.dp.DropTokenHash || got.PickupTokenHash != created.dp.PickupTokenHash {
		t.Error("token hashes did not round-trip")
	}
}

func TestCreateDropPointClientName(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()

	// A provided client name round-trips; an empty one is stored as NULL and
	// reads back as the empty string.
	withName, err := s.CreateDropPoint(ctx, domain.CreateParams{
		ID:              "dp_named",
		APITokenID:      "main",
		ClientName:      "generic-client",
		DropTokenHash:   "sha256:d1",
		PickupTokenHash: "sha256:p1",
		MaxBytes:        1024,
		CreatedAt:       baseTime,
		ExpiresAt:       baseTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	if withName.ClientName != "generic-client" {
		t.Errorf("client_name = %q, want %q", withName.ClientName, "generic-client")
	}
	got, err := s.GetDropPoint(ctx, "dp_named")
	if err != nil {
		t.Fatalf("GetDropPoint: %v", err)
	}
	if got.ClientName != "generic-client" {
		t.Errorf("round-tripped client_name = %q, want %q", got.ClientName, "generic-client")
	}

	noName := create(t, s, createOpts{id: "dp_unnamed", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	if noName.dp.ClientName != "" {
		t.Errorf("absent client_name = %q, want empty string", noName.dp.ClientName)
	}
	// Confirm the absent label is stored as SQL NULL, not an empty string.
	var clientName sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT client_name FROM drop_points WHERE id = ?`, "dp_unnamed").Scan(&clientName); err != nil {
		t.Fatalf("read client_name: %v", err)
	}
	if clientName.Valid {
		t.Errorf("absent client_name stored as %q, want NULL", clientName.String)
	}
}

func TestCreateDropPointValidatesParams(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	_, err := s.CreateDropPoint(context.Background(), domain.CreateParams{ID: ""})
	if !errors.Is(err, domain.ErrInvalidParams) {
		t.Fatalf("CreateDropPoint with empty params error = %v, want ErrInvalidParams", err)
	}
}

func TestGetDropPointNotFound(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	_, err := s.GetDropPoint(context.Background(), "dp_missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetDropPoint(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestFindOpenByDropTokenHash(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	got, err := s.FindOpenByDropTokenHash(ctx, token.Hash(created.rawDrop))
	if err != nil {
		t.Fatalf("FindOpenByDropTokenHash: %v", err)
	}
	if got.ID != created.dp.ID {
		t.Errorf("found id = %q, want %q", got.ID, created.dp.ID)
	}

	if _, err := s.FindOpenByDropTokenHash(ctx, token.Hash("drop_unknown")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown hash error = %v, want ErrNotFound", err)
	}

	// Once claimed (no longer open) the read no longer matches.
	if _, err := s.BeginReceiving(ctx, token.Hash(created.rawDrop), baseTime); err != nil {
		t.Fatalf("BeginReceiving: %v", err)
	}
	if _, err := s.FindOpenByDropTokenHash(ctx, token.Hash(created.rawDrop)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("non-open hash error = %v, want ErrNotFound", err)
	}
}

func TestAuthorizePickup(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	got, err := s.AuthorizePickup(ctx, created.dp.ID, token.Hash(created.rawPickup))
	if err != nil {
		t.Fatalf("AuthorizePickup with correct token: %v", err)
	}
	if got.ID != created.dp.ID {
		t.Errorf("authorized id = %q, want %q", got.ID, created.dp.ID)
	}

	if _, err := s.AuthorizePickup(ctx, created.dp.ID, token.Hash("pick_wrong")); !errors.Is(err, domain.ErrTokenMismatch) {
		t.Errorf("wrong token error = %v, want ErrTokenMismatch", err)
	}
	// A drop token must never authorize pickup.
	if _, err := s.AuthorizePickup(ctx, created.dp.ID, token.Hash(created.rawDrop)); !errors.Is(err, domain.ErrTokenMismatch) {
		t.Errorf("drop token used for pickup error = %v, want ErrTokenMismatch", err)
	}
	if _, err := s.AuthorizePickup(ctx, "dp_missing", token.Hash(created.rawPickup)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown id error = %v, want ErrNotFound", err)
	}
}

// TestCrossDropPointPickupRejected proves a pickup token for one drop point
// cannot authorize another (Phase 1 acceptance criterion).
func TestCrossDropPointPickupRejected(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	a := create(t, s, createOpts{id: "dp_a", apiTokenID: "desktop-main", createdAt: baseTime, ttl: time.Hour})
	b := create(t, s, createOpts{id: "dp_b", apiTokenID: "desktop-main", createdAt: baseTime, ttl: time.Hour})

	if _, err := s.AuthorizePickup(ctx, a.dp.ID, token.Hash(b.rawPickup)); !errors.Is(err, domain.ErrTokenMismatch) {
		t.Errorf("B's pickup token against A: error = %v, want ErrTokenMismatch", err)
	}
	if _, err := s.AuthorizePickup(ctx, b.dp.ID, token.Hash(a.rawPickup)); !errors.Is(err, domain.ErrTokenMismatch) {
		t.Errorf("A's pickup token against B: error = %v, want ErrTokenMismatch", err)
	}
	// Each token still authorizes its own drop point.
	if _, err := s.AuthorizePickup(ctx, a.dp.ID, token.Hash(a.rawPickup)); err != nil {
		t.Errorf("A's pickup token against A: %v", err)
	}
}

func TestCountActiveByAPIToken(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()

	// Three active drop points for "main": one open, one receiving, one ready.
	open := create(t, s, createOpts{id: "dp_open", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	recv := create(t, s, createOpts{id: "dp_recv", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	ready := create(t, s, createOpts{id: "dp_ready", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	_ = open

	if _, err := s.BeginReceiving(ctx, token.Hash(recv.rawDrop), baseTime); err != nil {
		t.Fatalf("begin recv: %v", err)
	}
	mustReady(t, s, ready)

	// A closed drop point for "main" must not count.
	closed := create(t, s, createOpts{id: "dp_closed", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	if _, err := s.CloseDropPoint(ctx, closed.dp.ID, baseTime); err != nil {
		t.Fatalf("close: %v", err)
	}
	// A drop point for another token must not count.
	create(t, s, createOpts{id: "dp_other", apiTokenID: "other", createdAt: baseTime, ttl: time.Hour})

	n, err := s.CountActiveByAPIToken(ctx, "main")
	if err != nil {
		t.Fatalf("CountActiveByAPIToken: %v", err)
	}
	if n != 3 {
		t.Errorf("active count for main = %d, want 3", n)
	}

	if n, err := s.CountActiveByAPIToken(ctx, "absent"); err != nil || n != 0 {
		t.Errorf("CountActiveByAPIToken(absent) = (%d, %v), want (0, nil)", n, err)
	}
}

func TestBeginCommitReceiving(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	recv, err := s.BeginReceiving(ctx, token.Hash(created.rawDrop), baseTime)
	if err != nil {
		t.Fatalf("BeginReceiving: %v", err)
	}
	if recv.Status != domain.StatusReceiving {
		t.Errorf("status after begin = %q, want receiving", recv.Status)
	}

	droppedAt := baseTime.Add(30 * time.Second)
	ready, err := s.CommitReceiving(ctx, domain.CommitParams{
		ID:            created.dp.ID,
		PayloadPath:   "drop-points/dp_default/payload.bin",
		EnvelopePath:  "drop-points/dp_default/envelope.json",
		EncryptedSize: 4096,
		DroppedAt:     droppedAt,
	})
	if err != nil {
		t.Fatalf("CommitReceiving: %v", err)
	}
	if ready.Status != domain.StatusReady {
		t.Errorf("status after commit = %q, want ready", ready.Status)
	}
	if ready.EncryptedSize == nil || *ready.EncryptedSize != 4096 {
		t.Errorf("encrypted_size = %v, want 4096", ready.EncryptedSize)
	}
	if ready.DroppedAt == nil || !ready.DroppedAt.Equal(droppedAt) {
		t.Errorf("dropped_at = %v, want %v", ready.DroppedAt, droppedAt)
	}
	if ready.PayloadPath == "" || ready.EnvelopePath == "" {
		t.Error("payload/envelope paths not recorded")
	}
}

func TestBeginReceivingSecondClaimRejected(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())
	dropHash := token.Hash(created.rawDrop)

	if _, err := s.BeginReceiving(ctx, dropHash, baseTime); err != nil {
		t.Fatalf("first BeginReceiving: %v", err)
	}
	// While receiving, a second claim is rejected: the slot is taken.
	if _, err := s.BeginReceiving(ctx, dropHash, baseTime); !errors.Is(err, domain.ErrAlreadyDropped) {
		t.Errorf("second claim while receiving error = %v, want ErrAlreadyDropped", err)
	}

	mustCommit(t, s, created.dp.ID, baseTime)
	// After a committed drop the slot stays consumed.
	if _, err := s.BeginReceiving(ctx, dropHash, baseTime); !errors.Is(err, domain.ErrAlreadyDropped) {
		t.Errorf("claim after commit error = %v, want ErrAlreadyDropped", err)
	}
}

// TestAbortReceivingReturnsToOpen proves an aborted receiving state returns the
// drop point to open without consuming the single-use slot (Phase 1 acceptance
// criterion).
func TestAbortReceivingReturnsToOpen(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())
	dropHash := token.Hash(created.rawDrop)

	if _, err := s.BeginReceiving(ctx, dropHash, baseTime); err != nil {
		t.Fatalf("BeginReceiving: %v", err)
	}
	aborted, err := s.AbortReceiving(ctx, created.dp.ID)
	if err != nil {
		t.Fatalf("AbortReceiving: %v", err)
	}
	if aborted.Status != domain.StatusOpen {
		t.Errorf("status after abort = %q, want open", aborted.Status)
	}

	// The slot is intact: the drop point can be received again and then committed.
	if _, err := s.BeginReceiving(ctx, dropHash, baseTime); err != nil {
		t.Fatalf("re-BeginReceiving after abort: %v", err)
	}
	if _, err := s.CommitReceiving(ctx, domain.CommitParams{
		ID:            created.dp.ID,
		PayloadPath:   "p",
		EnvelopePath:  "e",
		EncryptedSize: 1,
		DroppedAt:     baseTime,
	}); err != nil {
		t.Fatalf("CommitReceiving after re-begin: %v", err)
	}
}

func TestAbortReceivingRequiresReceiving(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	// Aborting an open (never-begun) drop point is an invalid transition.
	if _, err := s.AbortReceiving(ctx, created.dp.ID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("abort of open drop point error = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.AbortReceiving(ctx, "dp_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("abort of unknown drop point error = %v, want ErrNotFound", err)
	}
}

func TestBeginReceivingExpired(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	// now at/after expiry: the open drop point is treated as expired.
	atExpiry := created.dp.ExpiresAt
	if _, err := s.BeginReceiving(ctx, token.Hash(created.rawDrop), atExpiry); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("begin at expiry error = %v, want ErrExpired", err)
	}
	past := created.dp.ExpiresAt.Add(time.Minute)
	if _, err := s.BeginReceiving(ctx, token.Hash(created.rawDrop), past); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("begin after expiry error = %v, want ErrExpired", err)
	}
}

func TestBeginReceivingUnknown(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	if _, err := s.BeginReceiving(context.Background(), token.Hash("drop_nope"), baseTime); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("begin unknown error = %v, want ErrNotFound", err)
	}
}

// TestClosePreventsLaterDrop proves closing before any drop blocks later drops.
func TestClosePreventsLaterDrop(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	if _, err := s.CloseDropPoint(ctx, created.dp.ID, baseTime); err != nil {
		t.Fatalf("CloseDropPoint: %v", err)
	}
	if _, err := s.BeginReceiving(ctx, token.Hash(created.rawDrop), baseTime); !errors.Is(err, domain.ErrClosed) {
		t.Errorf("begin after close error = %v, want ErrClosed", err)
	}
}

func TestCommitRequiresReceiving(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	params := domain.CommitParams{
		ID:            created.dp.ID,
		PayloadPath:   "p",
		EnvelopePath:  "e",
		EncryptedSize: 1,
		DroppedAt:     baseTime,
	}
	// Committing an open (not yet receiving) drop point is rejected.
	if _, err := s.CommitReceiving(ctx, params); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("commit of open drop point error = %v, want ErrInvalidTransition", err)
	}
	// Committing an unknown drop point is not found.
	missing := params
	missing.ID = "dp_missing"
	if _, err := s.CommitReceiving(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("commit of unknown drop point error = %v, want ErrNotFound", err)
	}
	// Params are validated before touching the database.
	if _, err := s.CommitReceiving(ctx, domain.CommitParams{ID: created.dp.ID}); !errors.Is(err, domain.ErrInvalidParams) {
		t.Errorf("commit with empty params error = %v, want ErrInvalidParams", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	closedAt := baseTime.Add(time.Minute)
	first, err := s.CloseDropPoint(ctx, created.dp.ID, closedAt)
	if err != nil {
		t.Fatalf("first close: %v", err)
	}
	if first.Status != domain.StatusClosed {
		t.Errorf("status after close = %q, want closed", first.Status)
	}
	if first.ClosedAt == nil || !first.ClosedAt.Equal(closedAt) {
		t.Errorf("closed_at = %v, want %v", first.ClosedAt, closedAt)
	}

	// A repeated close succeeds and does not move closed_at.
	second, err := s.CloseDropPoint(ctx, created.dp.ID, baseTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if second.Status != domain.StatusClosed {
		t.Errorf("status after second close = %q, want closed", second.Status)
	}
	if second.ClosedAt == nil || !second.ClosedAt.Equal(closedAt) {
		t.Errorf("closed_at changed on repeat close: got %v, want %v", second.ClosedAt, closedAt)
	}
}

func TestCloseFromReceivingAndReady(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()

	// receiving -> closed
	recv := create(t, s, createOpts{id: "dp_recv", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	if _, err := s.BeginReceiving(ctx, token.Hash(recv.rawDrop), baseTime); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if dp, err := s.CloseDropPoint(ctx, recv.dp.ID, baseTime); err != nil || dp.Status != domain.StatusClosed {
		t.Fatalf("close from receiving = (%v, %v), want closed", dp, err)
	}

	// ready -> closed
	ready := create(t, s, createOpts{id: "dp_ready", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	mustReady(t, s, ready)
	if dp, err := s.CloseDropPoint(ctx, ready.dp.ID, baseTime); err != nil || dp.Status != domain.StatusClosed {
		t.Fatalf("close from ready = (%v, %v), want closed", dp, err)
	}
}

func TestCloseUnknown(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	if _, err := s.CloseDropPoint(context.Background(), "dp_missing", baseTime); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("close unknown error = %v, want ErrNotFound", err)
	}
}

func TestExpireDropPoints(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()

	// Two already-expired (TTL in the past relative to `now`) and one still live.
	expiredOpen := create(t, s, createOpts{id: "dp_exp_open", apiTokenID: "main", createdAt: baseTime, ttl: time.Minute})
	expiredReady := create(t, s, createOpts{id: "dp_exp_ready", apiTokenID: "main", createdAt: baseTime, ttl: time.Minute})
	mustReady(t, s, expiredReady)
	live := create(t, s, createOpts{id: "dp_live", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})

	// A terminal (closed) drop point must be left untouched by expiry.
	closed := create(t, s, createOpts{id: "dp_closed", apiTokenID: "main", createdAt: baseTime, ttl: time.Minute})
	if _, err := s.CloseDropPoint(ctx, closed.dp.ID, baseTime); err != nil {
		t.Fatalf("close: %v", err)
	}

	now := baseTime.Add(2 * time.Minute) // past the 1-minute TTLs, before the 1-hour TTL
	ids, err := s.ExpireDropPoints(ctx, now)
	if err != nil {
		t.Fatalf("ExpireDropPoints: %v", err)
	}
	gotExpired := map[string]bool{}
	for _, id := range ids {
		gotExpired[id] = true
	}
	if !gotExpired[expiredOpen.dp.ID] || !gotExpired[expiredReady.dp.ID] {
		t.Errorf("expired ids = %v, want both %q and %q", ids, expiredOpen.dp.ID, expiredReady.dp.ID)
	}
	if gotExpired[live.dp.ID] {
		t.Errorf("live drop point %q was expired", live.dp.ID)
	}
	if len(ids) != 2 {
		t.Errorf("expired %d drop points, want 2 (%v)", len(ids), ids)
	}

	// Statuses reflect the change; the closed drop point is still closed.
	if dp, _ := s.GetDropPoint(ctx, expiredReady.dp.ID); dp.Status != domain.StatusExpired {
		t.Errorf("expired ready status = %q, want expired", dp.Status)
	}
	if dp, _ := s.GetDropPoint(ctx, closed.dp.ID); dp.Status != domain.StatusClosed {
		t.Errorf("closed status after expiry sweep = %q, want closed", dp.Status)
	}

	// Idempotent: a second sweep at the same time finds nothing new.
	again, err := s.ExpireDropPoints(ctx, now)
	if err != nil {
		t.Fatalf("second ExpireDropPoints: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep expired %d drop points, want 0 (%v)", len(again), again)
	}
}

func TestRecordFirstPickup(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	// Pickup before a drop is rejected: nothing is ready.
	if _, err := s.RecordFirstPickup(ctx, created.dp.ID, baseTime); !errors.Is(err, domain.ErrNotReady) {
		t.Errorf("pickup before drop error = %v, want ErrNotReady", err)
	}

	mustReady(t, s, created)

	pickedAt := baseTime.Add(time.Minute)
	first, err := s.RecordFirstPickup(ctx, created.dp.ID, pickedAt)
	if err != nil {
		t.Fatalf("first pickup: %v", err)
	}
	if first.Status != domain.StatusReady {
		t.Errorf("status after pickup = %q, want ready (pickup must not change status)", first.Status)
	}
	if first.FirstPickedUpAt == nil || !first.FirstPickedUpAt.Equal(pickedAt) {
		t.Errorf("first_picked_up_at = %v, want %v", first.FirstPickedUpAt, pickedAt)
	}

	// A repeated pickup is allowed and does not move first_picked_up_at.
	second, err := s.RecordFirstPickup(ctx, created.dp.ID, pickedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("second pickup: %v", err)
	}
	if second.FirstPickedUpAt == nil || !second.FirstPickedUpAt.Equal(pickedAt) {
		t.Errorf("first_picked_up_at changed on repeat pickup: got %v, want %v", second.FirstPickedUpAt, pickedAt)
	}
}

func TestRecordFirstPickupRejectsClosedAndExpired(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()

	closed := create(t, s, createOpts{id: "dp_closed", apiTokenID: "main", createdAt: baseTime, ttl: time.Hour})
	mustReady(t, s, closed)
	if _, err := s.CloseDropPoint(ctx, closed.dp.ID, baseTime); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.RecordFirstPickup(ctx, closed.dp.ID, baseTime); !errors.Is(err, domain.ErrClosed) {
		t.Errorf("pickup of closed error = %v, want ErrClosed", err)
	}

	// Ready but past expiry: pickup must be rejected (SPEC §5, §7.5).
	expired := create(t, s, createOpts{id: "dp_expired", apiTokenID: "main", createdAt: baseTime, ttl: time.Minute})
	mustReady(t, s, expired)
	past := expired.dp.ExpiresAt.Add(time.Minute)
	if _, err := s.RecordFirstPickup(ctx, expired.dp.ID, past); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("pickup of expired ready error = %v, want ErrExpired", err)
	}

	if _, err := s.RecordFirstPickup(ctx, "dp_missing", baseTime); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("pickup of unknown error = %v, want ErrNotFound", err)
	}
}

// TestRawTokensNotStored verifies the database holds only token hashes, never raw
// token values (SPEC §6.4, §8).
func TestRawTokensNotStored(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())

	var dropHash, pickupHash string
	if err := s.db.QueryRowContext(ctx,
		`SELECT drop_token_hash, pickup_token_hash FROM drop_points WHERE id = ?`, created.dp.ID).
		Scan(&dropHash, &pickupHash); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if dropHash == created.rawDrop || pickupHash == created.rawPickup {
		t.Fatal("raw token stored in database row")
	}
	if dropHash != token.Hash(created.rawDrop) || pickupHash != token.Hash(created.rawPickup) {
		t.Error("stored hashes do not match token.Hash of the raw tokens")
	}
}

// TestBeginReceivingConcurrentSingleClaim proves the guarded UPDATE admits at
// most one winner under concurrency (SPEC §7.3; cross-phase one-drop race test).
func TestBeginReceivingConcurrentSingleClaim(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	ctx := context.Background()
	created := create(t, s, defaultCreateOpts())
	dropHash := token.Hash(created.rawDrop)

	const goroutines = 12
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.BeginReceiving(ctx, dropHash, baseTime); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if successes != 1 {
		t.Errorf("concurrent BeginReceiving had %d winners, want exactly 1", successes)
	}
	if dp, _ := s.GetDropPoint(ctx, created.dp.ID); dp.Status != domain.StatusReceiving {
		t.Errorf("status after concurrent claim = %q, want receiving", dp.Status)
	}
}

// mustReady drives a freshly created drop point to the ready state.
func mustReady(t *testing.T, s *Store, d dropPoint) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.BeginReceiving(ctx, token.Hash(d.rawDrop), baseTime); err != nil {
		t.Fatalf("mustReady begin: %v", err)
	}
	mustCommit(t, s, d.dp.ID, baseTime)
}

func mustCommit(t *testing.T, s *Store, id string, droppedAt time.Time) {
	t.Helper()
	if _, err := s.CommitReceiving(context.Background(), domain.CommitParams{
		ID:            id,
		PayloadPath:   "drop-points/" + id + "/payload.bin",
		EnvelopePath:  "drop-points/" + id + "/envelope.json",
		EncryptedSize: 16,
		DroppedAt:     droppedAt,
	}); err != nil {
		t.Fatalf("mustCommit: %v", err)
	}
}
