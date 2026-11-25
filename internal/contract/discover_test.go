package contract_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

type pagedChildren struct {
	Data   []contract.Session `json:"data"`
	Cursor struct {
		Next *string `json:"next"`
	} `json:"cursor"`
}

type bootstrapFixture struct {
	Roots    map[string]string                                      `json:"roots"`
	Get      map[string]struct{ Data contract.Session }             `json:"get"`
	Children map[string]pagedChildren                               `json:"children"`
	Paged    []pagedChildren                                        `json:"children_paged"`
	Perm     map[string]struct{ Data []contract.PermissionRequest } `json:"permission"`
	Form     map[string]struct{ Data []contract.Form }              `json:"form"`
}

// fakeAPI answers from recorded data and reproduces observed behavior: cursor.next exists whenever the page has data
// (even on the last page), so callers must detect the end by "page size < limit" or an "empty page".
type fakeAPI struct {
	fx          bootstrapFixture
	calls       []string
	failPermFor string
}

func (f *fakeAPI) Children(parentID, cursor string, limit int) ([]contract.Session, string, error) {
	f.calls = append(f.calls, "children:"+parentID)
	if parentID == f.fx.Roots["R"] && limit == 1 {
		i := 0
		if cursor != "" {
			i, _ = strconv.Atoi(cursor)
		}
		p := f.fx.Paged[i]
		next := ""
		if len(p.Data) > 0 {
			next = strconv.Itoa(i + 1)
		}
		return p.Data, next, nil
	}
	c, ok := f.fx.Children[parentID]
	if !ok {
		return nil, "", contract.ErrSessionNotFound
	}
	next := ""
	if len(c.Data) > 0 {
		next = "more"
	}
	if cursor == "more" {
		return nil, "", nil
	}
	return c.Data, next, nil
}

func (f *fakeAPI) Permissions(sessionID string) ([]contract.PermissionRequest, error) {
	f.calls = append(f.calls, "permission:"+sessionID)
	if sessionID == f.failPermFor {
		return nil, errors.New("boom")
	}
	return f.fx.Perm[sessionID].Data, nil
}

func (f *fakeAPI) Forms(sessionID string) ([]contract.Form, error) {
	f.calls = append(f.calls, "form:"+sessionID)
	return f.fx.Form[sessionID].Data, nil
}

func (f *fakeAPI) Session(id string) (contract.Session, error) {
	s, ok := f.fx.Get[id]
	if !ok {
		return contract.Session{}, contract.ErrSessionNotFound
	}
	return s.Data, nil
}

func (f *fakeAPI) Active() (map[string]bool, error) { return map[string]bool{}, nil }

func newFake(t *testing.T) *fakeAPI {
	t.Helper()
	var fx bootstrapFixture
	if err := json.Unmarshal(fixture(t, "bootstrap.json"), &fx); err != nil {
		t.Fatal(err)
	}
	return &fakeAPI{fx: fx}
}

// Mid-flight start: R's grandchild G already has a pending approval before startup, and grandchild C2 has a pending answer.
// Querying only the tab itself (R) shows no blocker; only discovering the whole family down via parentID yields WaitingApproval.
func TestDiscoverFamilyFindsPreexistingDescendantsAndBlockers(t *testing.T) {
	api := newFake(t)
	roots := api.fx.Roots

	// Control: querying only the tab itself sees nothing (which is exactly what the spec forbids).
	own, _ := api.Permissions(roots["R"])
	ownForms, _ := api.Forms(roots["R"])
	if len(own)+len(ownForms) != 0 {
		t.Fatalf("fixture precondition failed: the tab itself should have no pending items")
	}

	snap, err := contract.DiscoverFamily(api, roots["R"], 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.TabStatus(roots["R"]); got != contract.WaitingApproval {
		t.Fatalf("TabStatus = %v, want WaitingApproval", got)
	}
	ids := map[string]bool{}
	for _, s := range snap.Sessions {
		ids[s.ID] = true
	}
	for _, want := range []string{"ses_R", "ses_C", "ses_G", "ses_C2"} {
		if !ids[want] {
			t.Errorf("family is missing %s: %v", want, ids)
		}
	}
	if ids["ses_X"] {
		t.Errorf("unrelated ses_X must not be included in the family")
	}
}

// A family follows descendants downward only; it does not follow ancestors or include siblings.
func TestDiscoverFamilyOfADescendantTabExcludesAncestorsAndSiblings(t *testing.T) {
	api := newFake(t)
	snap, err := contract.DiscoverFamily(api, "ses_C", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.TabStatus("ses_C"); got != contract.WaitingApproval { // from grandchild G
		t.Fatalf("ses_C = %v", got)
	}
	for _, s := range snap.Sessions {
		if s.ID == "ses_R" || s.ID == "ses_C2" {
			t.Fatalf("ancestors/siblings must not be included: %s", s.ID)
		}
	}
}

// Pagination: walking page by page with limit=1 must still find all children.
func TestDiscoverFamilyWalksEveryPage(t *testing.T) {
	api := newFake(t)
	snap, err := contract.DiscoverFamily(api, "ses_R", 1)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range snap.Sessions {
		if s.ParentID == "ses_R" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("R's direct children = %d, want 2", n)
	}
}

// A failed single query must not be treated as "no pending items", otherwise incomplete data would be misreported as idle.
func TestDiscoverFamilyReportsIncompleteDataInsteadOfIdle(t *testing.T) {
	api := newFake(t)
	api.failPermFor = "ses_G"
	_, err := contract.DiscoverFamily(api, "ses_R", 100)
	if err == nil {
		t.Fatal("a failed permission query should report an error")
	}
	if errors.Is(err, contract.ErrSessionNotFound) {
		t.Fatal("a transient error must not be misjudged as the session not existing")
	}
}

// An explicit "session does not exist" must be distinguished from ordinary errors (keep the tab and show a data problem).
func TestDiscoverFamilyDistinguishesMissingSession(t *testing.T) {
	api := newFake(t)
	_, err := contract.DiscoverFamily(api, "ses_missing", 100)
	if !errors.Is(err, contract.ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}
