package grouplog

import (
	"encoding/base64"
	"net/http"
	"strconv"

	"runer-api/internal/auth"
	"runer-api/internal/httpx"
	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

type Handler struct {
	service *Service
	groups  *store.Groups
}

func NewHandler(service *Service, groups *store.Groups) *Handler {
	return &Handler{service: service, groups: groups}
}

func (h *Handler) RegisterRoutes(g *echo.Group, requireSession echo.MiddlewareFunc) {
	g.GET("/groups", h.listGroups, requireSession)
	g.POST("/groups", h.createGroup, requireSession)
	g.GET("/groups/:id/log", h.getLog, requireSession)
	g.POST("/groups/:id/log", h.appendLog, requireSession)
	g.GET("/groups/:id/members", h.listMembers, requireSession)
	g.DELETE("/groups/:id", h.deleteGroup, requireSession)
}

type groupView struct {
	ID       string `json:"id"`
	HeadSeq  int64  `json:"head_seq"`
	HeadHash string `json:"head_hash"`
	Epoch    int64  `json:"epoch"`
}

func toGroupView(g store.Group) groupView {
	return groupView{ID: g.ID, HeadSeq: g.HeadSeq, HeadHash: base64.RawURLEncoding.EncodeToString(g.HeadHash), Epoch: g.Epoch}
}

func (h *Handler) listGroups(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	groups, err := h.groups.ListForAccount(c.Request().Context(), acc.ID)
	if err != nil {
		return err
	}
	views := make([]groupView, len(groups))
	for i, g := range groups {
		views[i] = toGroupView(g)
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

// createGroupRequest carries only the signed genesis (E2EE §4.1).
type createGroupRequest struct {
	Entry string `json:"entry" validate:"required"`
	Sig   string `json:"sig" validate:"required"`
}

func (h *Handler) createGroup(c *echo.Context) error {
	var req createGroupRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	raw, sig, err := decodeEntryFields(req.Entry, req.Sig)
	if err != nil {
		return err
	}

	acc, _ := auth.AccountFromContext(c)
	g, err := h.service.CreateGroup(c.Request().Context(), acc.ID, raw, sig)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusCreated, toGroupView(g))
}

type logEntryView struct {
	Seq   int64  `json:"seq"`
	Entry string `json:"entry"`
	Sig   string `json:"sig"`
	Hash  string `json:"hash"`
}

// requireMember returns the caller's role in the list via the shared
// access check (live list, current member); anything else is 403, unknown
// lists included, so ids can't be probed.
func (h *Handler) requireMember(c *echo.Context, groupID string) (string, error) {
	acc, _ := auth.AccountFromContext(c)
	_, role, err := h.groups.Access(c.Request().Context(), groupID, acc.ID)
	return role, err
}

// logPageSize caps one log read. A full page means there may be more:
// the client asks again from the last seq + 1.
const logPageSize = 500

func (h *Handler) getLog(c *echo.Context) error {
	groupID := c.Param("id")
	if _, err := h.requireMember(c, groupID); err != nil {
		return err
	}
	from := int64(0)
	if s := c.QueryParam("from"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid from")
		}
		from = v
	}

	entries, err := h.groups.ListLogFrom(c.Request().Context(), groupID, from, logPageSize)
	if err != nil {
		return err
	}
	views := make([]logEntryView, len(entries))
	for i, e := range entries {
		views[i] = logEntryView{
			Seq:   e.Seq,
			Entry: base64.RawURLEncoding.EncodeToString(e.Entry),
			Sig:   base64.RawURLEncoding.EncodeToString(e.Sig),
			Hash:  base64.RawURLEncoding.EncodeToString(e.Hash),
		}
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

type appendLogRequest struct {
	Entry string `json:"entry" validate:"required"`
	Sig   string `json:"sig" validate:"required"`
}

func (h *Handler) appendLog(c *echo.Context) error {
	groupID := c.Param("id")
	if _, err := h.requireMember(c, groupID); err != nil {
		return err
	}
	var req appendLogRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	raw, sig, err := decodeEntryFields(req.Entry, req.Sig)
	if err != nil {
		return err
	}

	acc, _ := auth.AccountFromContext(c)
	g, err := h.service.AppendEntry(c.Request().Context(), acc.ID, groupID, raw, sig)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, toGroupView(g))
}

type memberView struct {
	AccountID       string `json:"account_id"`
	IdentitySignPub string `json:"identity_sign_pub"`
	Role            string `json:"role"`
}

func (h *Handler) listMembers(c *echo.Context) error {
	groupID := c.Param("id")
	if _, err := h.requireMember(c, groupID); err != nil {
		return err
	}
	members, err := h.groups.ListMembersWithKeys(c.Request().Context(), groupID)
	if err != nil {
		return err
	}
	views := make([]memberView, len(members))
	for i, m := range members {
		views[i] = memberView{AccountID: m.AccountID, IdentitySignPub: base64.RawURLEncoding.EncodeToString(m.IdentitySignPub), Role: m.Role}
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

// deleteGroup soft-deletes the list and all its content, owner only
// (API Reference).
func (h *Handler) deleteGroup(c *echo.Context) error {
	groupID := c.Param("id")
	role, err := h.requireMember(c, groupID)
	if err != nil {
		return err
	}
	if role != string(RoleOwner) {
		return echo.NewHTTPError(http.StatusForbidden, "forbidden")
	}

	acc, _ := auth.AccountFromContext(c)
	if err := h.service.DeleteGroup(c.Request().Context(), groupID, acc.ID); err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{"deleted": true})
}

func decodeEntryFields(entry, sig string) (raw, sigBytes []byte, err error) {
	raw, err = base64.RawURLEncoding.DecodeString(entry)
	if err != nil {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "invalid entry encoding")
	}
	sigBytes, err = base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "invalid sig encoding")
	}
	return raw, sigBytes, nil
}
