package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/a-jay98/stockpot/internal/identity"
	"github.com/a-jay98/stockpot/internal/recipes/chat"
)

// CodeChatProposalSaved answers dismissing a proposal that is already a version.
const CodeChatProposalSaved = "chat_proposal_saved"

// RecipeChatService is the part of chat.Service the API uses.
type RecipeChatService interface {
	List(ctx context.Context, hh, recipe uuid.UUID) ([]chat.Message, error)
	Post(ctx context.Context, hh, user, recipe uuid.UUID, text string) ([]chat.Message, error)
	Dismiss(ctx context.Context, hh, recipe, id uuid.UUID) (chat.Message, error)
}

var _ RecipeChatService = (*chat.Service)(nil)

func chatProblem(ctx context.Context, err error) (Problem, bool) {
	switch {
	case errors.Is(err, chat.ErrNotFound):
		return problemCtx(ctx, http.StatusNotFound, CodeNotFound, "That recipe or message isn't here any more."), true
	case errors.Is(err, chat.ErrInvalid):
		return problemCtx(ctx, http.StatusUnprocessableEntity, CodeInvalidValue, err.Error()), true
	case errors.Is(err, chat.ErrSaved):
		return problemCtx(ctx, http.StatusConflict, CodeChatProposalSaved, "That proposal is already saved as a version."), true
	}
	return LLMProblem(ctx, err)
}

func chatMessageOut(m chat.Message) RecipeChatMessage {
	out := RecipeChatMessage{
		Id: m.ID, RecipeId: m.RecipeID, VersionId: m.VersionID, VersionN: m.VersionN,
		Role: RecipeChatMessageRole(m.Role), Text: m.Text, ProposalState: RecipeChatMessageProposalState(m.State), ProposalError: m.ProposalError,
		SavedVersionId: m.SavedVersionID, SavedVersionN: m.SavedVersionN, UserId: m.UserID, CreatedAt: m.CreatedAt,
	}
	if p := m.Proposal; p != nil {
		out.Proposal = &RecipeChatProposal{
			ChangeSummary: p.ChangeSummary, Title: p.Title, Servings: p.Servings, TotalMinutes: p.TotalMinutes,
			Ingredients: p.Ingredients, Notes: p.Notes,
		}
		if p.Steps != nil {
			steps := make([]RecipeStep, 0, len(*p.Steps))
			for _, s := range *p.Steps {
				steps = append(steps, RecipeStep{Text: s.Text, Minutes: s.Minutes})
			}
			out.Proposal.Steps = &steps
		}
	}
	return out
}

func chatOut(ms []chat.Message) RecipeChat {
	out := RecipeChat{Messages: make([]RecipeChatMessage, 0, len(ms))}
	for _, m := range ms {
		out.Messages = append(out.Messages, chatMessageOut(m))
	}
	return out
}

// ListRecipeChat implements StrictServerInterface.
func (s *Server) ListRecipeChat(ctx context.Context, req ListRecipeChatRequestObject) (ListRecipeChatResponseObject, error) {
	if s.chat == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := s.chat.List(ctx, hh, req.Id)
	if p, ok := chatProblem(ctx, err); ok {
		return ListRecipeChatdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return ListRecipeChat200JSONResponse(chatOut(ms)), nil
}

// PostRecipeChat implements StrictServerInterface.
func (s *Server) PostRecipeChat(ctx context.Context, req PostRecipeChatRequestObject) (PostRecipeChatResponseObject, error) {
	if s.chat == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := s.chat.Post(ctx, hh, identity.PrincipalFrom(ctx).UserID, req.Id, req.Body.Text)
	if p, ok := chatProblem(ctx, err); ok {
		return PostRecipeChatdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return PostRecipeChat200JSONResponse(chatOut(ms)), nil
}

// DismissRecipeChatProposal implements StrictServerInterface.
func (s *Server) DismissRecipeChatProposal(ctx context.Context, req DismissRecipeChatProposalRequestObject) (DismissRecipeChatProposalResponseObject, error) {
	if s.chat == nil {
		return nil, errNotWired
	}
	hh, err := identity.HouseholdID(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.chat.Dismiss(ctx, hh, req.Id, req.MessageId)
	if p, ok := chatProblem(ctx, err); ok {
		return DismissRecipeChatProposaldefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	}
	if err != nil {
		return nil, err
	}
	return DismissRecipeChatProposal200JSONResponse(chatMessageOut(m)), nil
}
