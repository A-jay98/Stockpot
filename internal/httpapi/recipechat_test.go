package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/llm"
	"github.com/a-jay98/stockpot/internal/recipes"
	"github.com/a-jay98/stockpot/internal/recipes/chat"
)

type fakeChat struct {
	err      error
	hh, user uuid.UUID
	text     string
}

func (f *fakeChat) msgs(recipe uuid.UUID) []chat.Message {
	n := 2
	return []chat.Message{
		{ID: uuid.New(), RecipeID: recipe, VersionID: uuid.New(), VersionN: 1, Role: chat.RoleUser, Text: f.text, State: chat.StateNone, UserID: &f.user, CreatedAt: time.Now()},
		{
			ID: uuid.New(), RecipeID: recipe, VersionID: uuid.New(), VersionN: 1, Role: chat.RoleAssistant, Text: "ok", State: chat.StateSaved,
			Proposal:  &chat.Proposal{ChangeSummary: "More paste", Content: recipes.Content{Title: "Stew", Steps: []recipes.Step{{Text: "Fry."}}}},
			CreatedAt: time.Now(), SavedVersionN: &n,
		},
	}
}

func (f *fakeChat) List(_ context.Context, hh, recipe uuid.UUID) ([]chat.Message, error) {
	f.hh = hh
	return f.msgs(recipe), f.err
}

func (f *fakeChat) Post(_ context.Context, hh, user, recipe uuid.UUID, text string) ([]chat.Message, error) {
	f.hh, f.user, f.text = hh, user, text
	return f.msgs(recipe), f.err
}

func (f *fakeChat) Dismiss(_ context.Context, hh, recipe, _ uuid.UUID) (chat.Message, error) {
	f.hh = hh
	return f.msgs(recipe)[1], f.err
}

func chatHandler(t *testing.T, f RecipeChatService, p *identity.Principal) http.Handler {
	t.Helper()
	opts := Options{PublicURL: testOrigin, Authorizer: allowAll{}}
	if p != nil {
		opts.Session = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
			})
		}
	}
	return newHandler(t, NewServer(Deps{Chat: f}), opts)
}

func TestRecipeChatEndpoints(t *testing.T) {
	p := &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive, HouseholdID: uuid.New()}
	f := &fakeChat{}
	h := chatHandler(t, f, p)
	base := "/api/v1/recipes/" + uuid.NewString() + "/chat"

	rec := do(h, llmRequest(t, http.MethodPost, base, `{"text":"More paste?"}`))
	var out RecipeChat
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Messages) != 2 {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if f.hh != p.HouseholdID || f.user != p.UserID || f.text != "More paste?" {
		t.Fatalf("post called with %+v", f)
	}
	ai := out.Messages[1]
	if ai.Proposal == nil || ai.Proposal.Ingredients == nil || ai.ProposalState != "saved" || *ai.SavedVersionN != 2 {
		t.Fatalf("reply %s", rec.Body)
	}
	if rec := do(h, llmRequest(t, http.MethodGet, base, "")); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, llmRequest(t, http.MethodPost, base, `{"text":""}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty text: %d", rec.Code)
	}

	dismiss := base + "/" + uuid.NewString() + "/dismiss"
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{nil, http.StatusOK, ""},
		{chat.ErrNotFound, http.StatusNotFound, CodeNotFound},
		{chat.ErrSaved, http.StatusConflict, CodeChatProposalSaved},
		{fmt.Errorf("%w: no", chat.ErrInvalid), http.StatusUnprocessableEntity, CodeInvalidValue},
	} {
		f.err = tc.err
		rec := do(h, llmRequest(t, http.MethodPost, dismiss, ""))
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("dismiss %v: %d %s", tc.err, rec.Code, rec.Body)
		}
	}
	for _, err := range []error{llm.ErrUnavailable, llm.ErrBadOutput} {
		f.err = err
		rec := do(h, llmRequest(t, http.MethodPost, base, `{"text":"hi"}`))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeLLMUnavailable) {
			t.Errorf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
}

func TestRecipeChatNeedsAHousehold(t *testing.T) {
	h := chatHandler(t, &fakeChat{}, &identity.Principal{UserID: uuid.New(), Status: identity.StatusActive})
	if rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/recipes/"+uuid.NewString()+"/chat", "")); rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("no household: %d %s", rec.Code, rec.Body)
	}
	h = newHandler(t, NewServer(Deps{Chat: &fakeChat{}}), Options{PublicURL: testOrigin})
	if rec := do(h, llmRequest(t, http.MethodGet, "/api/v1/recipes/"+uuid.NewString()+"/chat", "")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", rec.Code)
	}
}
