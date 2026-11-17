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

// fakeAPI 以錄製資料回應，並重現實測行為：只要本頁有資料，cursor.next 就存在
// （即使已是最後一頁），所以呼叫端必須以「本頁筆數 < limit」或「空頁」判斷結束。
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

// 中途啟動：R 的孫輩 G 在啟動前就有待核准、孫輩 C2 有待回答。
// 只查 tab 本身（R）看不到任何阻塞；沿 parentID 向下發現整個家族才能得到 WaitingApproval。
func TestDiscoverFamilyFindsPreexistingDescendantsAndBlockers(t *testing.T) {
	api := newFake(t)
	roots := api.fx.Roots

	// 對照組：只查 tab 本身，什麼都看不到（這正是 spec 禁止的做法）。
	own, _ := api.Permissions(roots["R"])
	ownForms, _ := api.Forms(roots["R"])
	if len(own)+len(ownForms) != 0 {
		t.Fatalf("fixture 前提不成立：tab 本身不應有待處理事項")
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
			t.Errorf("家族缺少 %s：%v", want, ids)
		}
	}
	if ids["ses_X"] {
		t.Errorf("無關的 ses_X 不應被納入家族")
	}
}

// 家族只往下追後代，不往上追祖先、不納入兄弟。
func TestDiscoverFamilyOfADescendantTabExcludesAncestorsAndSiblings(t *testing.T) {
	api := newFake(t)
	snap, err := contract.DiscoverFamily(api, "ses_C", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.TabStatus("ses_C"); got != contract.WaitingApproval { // 來自孫輩 G
		t.Fatalf("ses_C = %v", got)
	}
	for _, s := range snap.Sessions {
		if s.ID == "ses_R" || s.ID == "ses_C2" {
			t.Fatalf("不應納入祖先／兄弟：%s", s.ID)
		}
	}
}

// 分頁：limit=1 逐頁走訪也要找齊所有子層。
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
		t.Fatalf("R 的直接子層 = %d, want 2", n)
	}
}

// 單項查詢失敗不能被當成「沒有待處理事項」，否則會把資料不完整誤報成閒置。
func TestDiscoverFamilyReportsIncompleteDataInsteadOfIdle(t *testing.T) {
	api := newFake(t)
	api.failPermFor = "ses_G"
	_, err := contract.DiscoverFamily(api, "ses_R", 100)
	if err == nil {
		t.Fatal("權限查詢失敗時應回報錯誤")
	}
	if errors.Is(err, contract.ErrSessionNotFound) {
		t.Fatal("暫時性錯誤不應被誤判為 session 不存在")
	}
}

// 明確的「session 不存在」要與一般錯誤區分（tab 保留、顯示資料異常）。
func TestDiscoverFamilyDistinguishesMissingSession(t *testing.T) {
	api := newFake(t)
	_, err := contract.DiscoverFamily(api, "ses_missing", 100)
	if !errors.Is(err, contract.ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}
