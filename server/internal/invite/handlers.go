package invite

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/httpx"
	"runer-api/internal/limits"

	"github.com/labstack/echo/v5"
)

type Handler struct {
	service *Service
	readIPs *limits.Keyed // unauthenticated package reads, per IP
}

func NewHandler(service *Service, readsPerIPPerMinute int) *Handler {
	return &Handler{service: service, readIPs: limits.PerMinute(readsPerIPPerMinute)}
}

func (h *Handler) RegisterRoutes(g *echo.Group, requireSession echo.MiddlewareFunc) {
	g.POST("/groups/:id/invites", h.createInvite, requireSession)
	g.GET("/groups/:id/join-requests", h.listJoinRequests, requireSession)
	g.GET("/invites/:invite_id", h.getPackage) // no auth: the invite id from the link IS the credential
	g.POST("/invites/:invite_id/join", h.join, requireSession)
	g.DELETE("/invites/:invite_id", h.revoke, requireSession)
	g.GET("/invites/:invite_id/log", h.inviteLog, requireSession)
	g.POST("/invites/:invite_id/accept", h.accept, requireSession)
}

type createInviteRequest struct {
	InviteID  string    `json:"invite_id" validate:"required"`
	Package   string    `json:"package" validate:"required"`
	ExpiresAt time.Time `json:"expires_at" validate:"required"`
	InvitePub string    `json:"invite_pub"` // link invites only (E2EE §6)
}

func (h *Handler) createInvite(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	groupID := c.Param("id")

	var req createInviteRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	pkg, err := base64.RawURLEncoding.DecodeString(req.Package)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid package encoding")
	}

	var invitePub []byte
	if req.InvitePub != "" {
		if invitePub, err = base64.RawURLEncoding.DecodeString(req.InvitePub); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid invite_pub encoding")
		}
	}

	inv, err := h.service.CreateInvite(c.Request().Context(), groupID, acc.ID, req.InviteID, pkg, req.ExpiresAt, invitePub)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusCreated, map[string]any{"id": inv.ID, "expires_at": inv.ExpiresAt})
}

func (h *Handler) getPackage(c *echo.Context) error {
	if !h.readIPs.Allow(c.RealIP()) {
		return auth.ErrRateLimited
	}
	pkg, err := h.service.GetPackage(c.Request().Context(), c.Param("invite_id"))
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{"package": base64.RawURLEncoding.EncodeToString(pkg)})
}

type joinRequest struct {
	EncDisplayName string `json:"enc_display_name" validate:"required"`
	MAC            string `json:"mac" validate:"required"`
}

func (h *Handler) join(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	inviteID := c.Param("invite_id")

	var req joinRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	encName, err := base64.RawURLEncoding.DecodeString(req.EncDisplayName)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid enc_display_name encoding")
	}
	mac, err := base64.RawURLEncoding.DecodeString(req.MAC)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid mac encoding")
	}

	jr, err := h.service.Join(c.Request().Context(), inviteID, acc.ID, encName, mac)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusCreated, map[string]any{"id": jr.ID})
}

type joinRequestView struct {
	ID               string `json:"id"`
	InviteID         string `json:"invite_id"`
	AccountID        string `json:"account_id"`
	IdentitySignPub  string `json:"identity_sign_pub"`
	IdentityAgreePub string `json:"identity_agree_pub"`
	EncDisplayName   string `json:"enc_display_name"`
	MAC              string `json:"mac"`
}

func (h *Handler) listJoinRequests(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	groupID := c.Param("id")

	requests, err := h.service.ListJoinRequests(c.Request().Context(), groupID, acc.ID)
	if err != nil {
		return err
	}
	views := make([]joinRequestView, len(requests))
	for i, jr := range requests {
		views[i] = joinRequestView{
			ID: jr.ID, InviteID: jr.InviteID, AccountID: jr.AccountID,
			IdentitySignPub:  base64.RawURLEncoding.EncodeToString(jr.IdentitySignPub),
			IdentityAgreePub: base64.RawURLEncoding.EncodeToString(jr.IdentityAgreePub),
			EncDisplayName:   base64.RawURLEncoding.EncodeToString(jr.EncDisplayName),
			MAC:              base64.RawURLEncoding.EncodeToString(jr.MAC),
		}
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

// logPageSize matches GET /groups/{id}/log: a full page means ask again.
const logPageSize = 500

func (h *Handler) inviteLog(c *echo.Context) error {
	from := int64(0)
	if s := c.QueryParam("from"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid from")
		}
		from = v
	}
	entries, err := h.service.InviteLog(c.Request().Context(), c.Param("invite_id"), from, logPageSize)
	if err != nil {
		return err
	}
	views := make([]map[string]any, len(entries))
	for i, e := range entries {
		views[i] = map[string]any{
			"seq":   e.Seq,
			"entry": base64.RawURLEncoding.EncodeToString(e.Entry),
			"sig":   base64.RawURLEncoding.EncodeToString(e.Sig),
			"hash":  base64.RawURLEncoding.EncodeToString(e.Hash),
		}
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

type acceptRequest struct {
	Entry string `json:"entry" validate:"required"`
	Sig   string `json:"sig" validate:"required"`
}

func (h *Handler) accept(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	var req acceptRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(req.Entry)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid entry encoding")
	}
	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sig encoding")
	}
	g, err := h.service.Accept(c.Request().Context(), c.Param("invite_id"), acc.ID, raw, sig)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"id": g.ID, "head_seq": g.HeadSeq, "head_hash": base64.RawURLEncoding.EncodeToString(g.HeadHash), "epoch": g.Epoch,
	})
}

func (h *Handler) revoke(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	if err := h.service.Revoke(c.Request().Context(), c.Param("invite_id"), acc.ID); err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{"revoked": true})
}
