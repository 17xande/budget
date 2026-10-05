package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/middleware"
	"github.com/17xande-dev/budget/internal/validate"
)

type membersData struct {
	page
	Members []auth.User
	Email   string
	Name    string
	Errors  validate.FormErrors
}

// Members lists who can sign in, with the form to add someone.
func (h *Handler) Members(w http.ResponseWriter, r *http.Request) {
	h.renderMembers(w, r, http.StatusOK, "", "", nil)
}

func (h *Handler) renderMembers(w http.ResponseWriter, r *http.Request, status int, email, name string, errs validate.FormErrors) {
	members, err := h.users.Users()
	if err != nil {
		log.Printf("members: list: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	h.render(w, status, "members.html", membersData{
		page:    newPage(r, "Members", "members"),
		Members: members, Email: email, Name: name, Errors: errs,
	})
}

// MemberCreate adds someone with a temporary password you choose and pass on.
// It is marked must-change, so their first sign-in goes straight to choosing
// their own: the person who typed this password is not the person who will use it.
func (h *Handler) MemberCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	email := validate.NormalizeEmail(r.PostFormValue("email"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	password := r.PostFormValue("password")

	errs := validate.ValidateMember(email, name)
	validate.ValidatePassword(errs, password, r.PostFormValue("password_confirm"))
	if len(errs) > 0 {
		h.renderMembers(w, r, http.StatusUnprocessableEntity, email, name, errs)
		return
	}
	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		log.Printf("members: hash password: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if _, err := h.users.CreateUser(email, name, hash, true, time.Now()); err != nil {
		if errors.Is(err, auth.ErrEmailTaken) {
			h.renderMembers(w, r, http.StatusUnprocessableEntity, email, name,
				validate.FormErrors{"email": "Someone with this email already has an account."})
			return
		}
		log.Printf("members: create: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	me, _ := middleware.User(r)
	log.Printf("members: %s added %s", me.Email, email)
	http.Redirect(w, r, "/members?notice=member_added", http.StatusSeeOther)
}

type passwordData struct {
	page
	Errors validate.FormErrors
}

func (h *Handler) OwnPasswordForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "password.html", passwordData{page: newPage(r, "Your password", "password")})
}

// OwnPasswordChange sets your own password after checking the current one, then
// ends every session including this one -- which is what shuts out anyone else
// holding an old one -- and sends you to sign in again.
func (h *Handler) OwnPasswordChange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	me, _ := middleware.User(r)
	again := func(status int, errs validate.FormErrors) {
		h.render(w, status, "password.html", passwordData{page: newPage(r, "Your password", "password"), Errors: errs})
	}

	password := r.PostFormValue("password")
	errs := make(validate.FormErrors)
	validate.ValidatePassword(errs, password, r.PostFormValue("password_confirm"))
	if len(errs) > 0 {
		again(http.StatusUnprocessableEntity, errs)
		return
	}
	ok, err := h.users.CheckUserPassword(me.ID, r.PostFormValue("current"))
	if err != nil {
		log.Printf("password: check current for %d: %v", me.ID, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		again(http.StatusUnauthorized, validate.FormErrors{"current": "That is not your current password."})
		return
	}
	// A temporary password set by someone else is the case the forced change
	// exists for; reusing it as "your own" would defeat it.
	if password == r.PostFormValue("current") {
		again(http.StatusUnprocessableEntity, validate.FormErrors{"password": "Choose a password different from the current one."})
		return
	}
	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		log.Printf("password: hash: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if _, err := h.users.SetPassword(me.ID, hash, false); err != nil {
		log.Printf("password: set for %d: %v", me.ID, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("password changed: %s", me.Email)
	http.SetCookie(w, h.sessionCookie("", -time.Hour))
	http.Redirect(w, r, "/login?notice=password_changed", http.StatusSeeOther)
}
