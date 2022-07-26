package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-logr/logr"
	"github.com/weaveworks/weave-gitops/api/v1alpha1"
	"github.com/weaveworks/weave-gitops/core/logger"
	"github.com/weaveworks/weave-gitops/pkg/featureflags"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LoginOIDC                 string = "oidc"
	LoginUsername             string = "username"
	ClusterUserAuthSecretName string = "cluster-user-auth"
	DefaultOIDCAuthSecretName string = "oidc-auth"
	FeatureFlagClusterUser    string = "CLUSTER_USER_AUTH"
	FeatureFlagOIDCAuth       string = "OIDC_AUTH"
	FeatureFlagSet            string = "true"
)

// OIDCConfig is used to configure an AuthServer to interact with
// an OIDC issuer.
type OIDCConfig struct {
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	TokenDuration time.Duration
}

// AuthConfig is used to configure an AuthServer.
type AuthConfig struct {
	Log                 logr.Logger
	client              *http.Client
	kubernetesClient    ctrlclient.Client
	tokenSignerVerifier TokenSignerVerifier
	config              OIDCConfig
}

// AuthServer interacts with an OIDC issuer to handle the OAuth2 process flow.
type AuthServer struct {
	AuthConfig
	provider *oidc.Provider
}

// LoginRequest represents the data submitted by client when the auth flow (non-OIDC) is used.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// UserInfo represents the response returned from the user info handler.
type UserInfo struct {
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

// func InitAuthServer(ctx context.Context, log logr.Logger, rawKubernetesClient ctrlclient.Client, oidcConfig OIDCConfig, oidcSecret, devUser string, devMode bool, authMethods []string) (*AuthServer, error) {
func InitAuthServer(ctx context.Context, log logr.Logger, rawKubernetesClient ctrlclient.Client, oidcConfig OIDCConfig, oidcSecret, devUser string, devMode bool) (*AuthServer, error) {
	if oidcSecret != DefaultOIDCAuthSecretName {
		log.V(logger.LogLevelDebug).Info("Reading OIDC configuration from alternate secret", "secretName", oidcSecret)
	}

	// If OIDC auth secret is found prefer that over CLI parameters
	var secret corev1.Secret
	if err := rawKubernetesClient.Get(ctx, client.ObjectKey{
		Namespace: v1alpha1.DefaultNamespace,
		Name:      oidcSecret,
	}, &secret); err == nil {
		if oidcConfig.ClientSecret != "" && secret.Data["clientSecret"] != nil { // 'Data' is a byte array
			log.V(logger.LogLevelWarn).Info("OIDC client configured by both CLI and secret. CLI values will be overridden.")
		}

		oidcConfig = NewOIDCConfigFromSecret(secret)
	} else if err != nil {
		log.V(logger.LogLevelDebug).Info("Could not read OIDC secret", "secretName", oidcSecret, "error", err)
	}

	if oidcConfig.ClientSecret != "" {
		log.V(logger.LogLevelDebug).Info("OIDC config", "IssuerURL", oidcConfig.IssuerURL, "ClientID", oidcConfig.ClientID, "ClientSecretLength", len(oidcConfig.ClientSecret), "RedirectURL", oidcConfig.RedirectURL, "TokenDuration", oidcConfig.TokenDuration)
	}

	tsv, err := NewHMACTokenSignerVerifier(oidcConfig.TokenDuration)
	if err != nil {
		return nil, fmt.Errorf("could not create HMAC token signer: %w", err)
	}

	if devMode {
		log.V(logger.LogLevelWarn).Info("Dev-mode is enabled. This should be used for local work only.")
		tsv.SetDevMode(devUser)
	}

	authCfg, err := NewAuthServerConfig(log, oidcConfig, rawKubernetesClient, tsv)
	if err != nil {
		return nil, err
	}

	authServer, err := NewAuthServer(ctx, authCfg)
	if err != nil {
		return nil, fmt.Errorf("could not create auth server: %w", err)
	}
	return authServer, err
}

func NewOIDCConfigFromSecret(secret corev1.Secret) OIDCConfig {
	cfg := OIDCConfig{
		IssuerURL:    string(secret.Data["issuerURL"]),
		ClientID:     string(secret.Data["clientID"]),
		ClientSecret: string(secret.Data["clientSecret"]),
		RedirectURL:  string(secret.Data["redirectURL"]),
	}

	tokenDuration, err := time.ParseDuration(string(secret.Data["tokenDuration"]))
	if err != nil {
		tokenDuration = time.Hour
	}

	cfg.TokenDuration = tokenDuration

	return cfg
}

func NewAuthServerConfig(log logr.Logger, oidcCfg OIDCConfig, kubernetesClient ctrlclient.Client, tsv TokenSignerVerifier) (AuthConfig, error) {
	if _, err := url.Parse(oidcCfg.IssuerURL); err != nil {
		return AuthConfig{}, fmt.Errorf("invalid issuer URL: %w", err)
	}

	if _, err := url.Parse(oidcCfg.RedirectURL); err != nil {
		return AuthConfig{}, fmt.Errorf("invalid redirect URL: %w", err)
	}

	return AuthConfig{
		Log:                 log.WithName("auth-server"),
		client:              http.DefaultClient,
		kubernetesClient:    kubernetesClient,
		tokenSignerVerifier: tsv,
		config:              oidcCfg,
	}, nil
}

// NewAuthServer creates a new AuthServer object.
func NewAuthServer(ctx context.Context, cfg AuthConfig) (*AuthServer, error) {
	var secret corev1.Secret
	err := cfg.kubernetesClient.Get(ctx, client.ObjectKey{
		Namespace: v1alpha1.DefaultNamespace,
		Name:      ClusterUserAuthSecretName,
	}, &secret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			featureflags.Set(FeatureFlagClusterUser, "false")
		} else {
			cfg.Log.Error(err, "could not get secret for cluster user")
		}
	} else {
		featureflags.Set(FeatureFlagClusterUser, FeatureFlagSet)
	}

	var provider *oidc.Provider

	if cfg.config.IssuerURL == "" {
		featureflags.Set(FeatureFlagOIDCAuth, "false")
	} else {
		var err error

		provider, err = oidc.NewProvider(ctx, cfg.config.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("could not create provider: %w", err)
		}
		featureflags.Set(FeatureFlagOIDCAuth, FeatureFlagSet)
	}

	if featureflags.Get(FeatureFlagOIDCAuth) != FeatureFlagSet && featureflags.Get(FeatureFlagClusterUser) != FeatureFlagSet {
		return nil, fmt.Errorf("Neither OIDC auth or local auth enabled, can't start")
	}

	return &AuthServer{cfg, provider}, nil
}

// SetRedirectURL is used to set the redirect URL. This is meant to be used
// in unit tests only.
func (s *AuthServer) SetRedirectURL(url string) {
	s.config.RedirectURL = url
}

func (s *AuthServer) oidcEnabled() bool {
	return featureflags.Get(FeatureFlagOIDCAuth) == FeatureFlagSet
}

func (s *AuthServer) verifier() *oidc.IDTokenVerifier {
	return s.provider.Verifier(&oidc.Config{ClientID: s.config.ClientID})
}

func (s *AuthServer) oauth2Config(scopes []string) *oauth2.Config {
	// Ensure "openid" scope is always present.
	if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append(scopes, oidc.ScopeOpenID)
	}

	// Request "email" scope to get user's email address.
	if !contains(scopes, scopeEmail) {
		scopes = append(scopes, scopeEmail)
	}

	// Request "groups" scope to get user's groups.
	if !contains(scopes, scopeGroups) {
		scopes = append(scopes, scopeGroups)
	}

	return &oauth2.Config{
		ClientID:     s.config.ClientID,
		ClientSecret: s.config.ClientSecret,
		Endpoint:     s.provider.Endpoint(),
		RedirectURL:  s.config.RedirectURL,
		Scopes:       scopes,
	}
}

func (s *AuthServer) OAuth2Flow() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !s.oidcEnabled() {
			JSONError(s.Log, rw, "oidc provider not configured", http.StatusBadRequest)
			return
		}

		s.startAuthFlow(rw, r)
	}
}

func (s *AuthServer) Callback() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		var (
			token *oauth2.Token
			state SessionState
		)

		if r.Method != http.MethodGet {
			rw.Header().Add("Allow", "GET")
			rw.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		ctx := oidc.ClientContext(r.Context(), s.client)

		// Authorization redirect callback from OAuth2 auth flow.
		if errorCode := r.FormValue("error"); errorCode != "" {
			s.Log.Info("authz redirect callback failed", "error", errorCode, "error_description", r.FormValue("error_description"))
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		code := r.FormValue("code")
		if code == "" {
			s.Log.Info("code value was empty")
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		cookie, err := r.Cookie(StateCookieName)
		if err != nil {
			s.Log.Error(err, "cookie was not found in the request", "cookie", StateCookieName)
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		if state := r.FormValue("state"); state != cookie.Value {
			s.Log.Info("cookie value does not match state form value")
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		b, err := base64.StdEncoding.DecodeString(cookie.Value)
		if err != nil {
			s.Log.Error(err, "cannot base64 decode cookie", "cookie", StateCookieName, "cookie_value", cookie.Value)
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		if err := json.Unmarshal(b, &state); err != nil {
			s.Log.Error(err, "failed to unmarshal state to JSON", "state", string(b))
			rw.WriteHeader(http.StatusBadRequest)

			return
		}

		token, err = s.oauth2Config(nil).Exchange(ctx, code)
		if err != nil {
			s.Log.Error(err, "failed to exchange auth code for token", "code", code)
			rw.WriteHeader(http.StatusInternalServerError)

			return
		}

		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok {
			JSONError(s.Log, rw, "no id_token in token response", http.StatusInternalServerError)
			return
		}

		_, err = s.verifier().Verify(r.Context(), rawIDToken)
		if err != nil {
			JSONError(s.Log, rw, fmt.Sprintf("failed to verify ID token: %v", err), http.StatusInternalServerError)
			return
		}

		// Issue ID token cookie
		http.SetCookie(rw, s.createCookie(IDTokenCookieName, rawIDToken))
		http.SetCookie(rw, s.createCookie(AccessTokenCookieName, token.AccessToken))

		// Clear state cookie
		http.SetCookie(rw, s.clearCookie(StateCookieName))

		http.Redirect(rw, r, state.ReturnURL, http.StatusSeeOther)
	}
}

func (s *AuthServer) SignIn() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rw.Header().Add("Allow", "POST")
			rw.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		var loginRequest LoginRequest

		err := json.NewDecoder(r.Body).Decode(&loginRequest)
		if err != nil {
			s.Log.Error(err, "Failed to decode from JSON")
			JSONError(s.Log, rw, "Failed to read request body.", http.StatusBadRequest)

			return
		}

		var hashedSecret corev1.Secret

		if err := s.kubernetesClient.Get(r.Context(), ctrlclient.ObjectKey{
			Namespace: v1alpha1.DefaultNamespace,
			Name:      ClusterUserAuthSecretName,
		}, &hashedSecret); err != nil {
			s.Log.Error(err, "Failed to query for the secret")
			JSONError(s.Log, rw, "Please ensure that a password has been set.", http.StatusBadRequest)

			return
		}

		if loginRequest.Username != string(hashedSecret.Data["username"]) {
			s.Log.Info("Wrong username")
			rw.WriteHeader(http.StatusUnauthorized)

			return
		}

		if err := bcrypt.CompareHashAndPassword(hashedSecret.Data["password"], []byte(loginRequest.Password)); err != nil {
			s.Log.Error(err, "Failed to compare hash with password")
			rw.WriteHeader(http.StatusUnauthorized)

			return
		}

		signed, err := s.tokenSignerVerifier.Sign(loginRequest.Username)
		if err != nil {
			s.Log.Error(err, "Failed to create and sign token")
			rw.WriteHeader(http.StatusInternalServerError)

			return
		}

		http.SetCookie(rw, s.createCookie(IDTokenCookieName, signed))
		rw.WriteHeader(http.StatusOK)
	}
}

// UserInfo inspects the cookie and attempts to verify it as an admin token. If successful,
// it returns a UserInfo object with the email set to the admin token subject. Otherwise it
// uses the token to query the OIDC provider's user info endpoint and return a UserInfo object
// back or a 401 status in any other case.
func (s *AuthServer) UserInfo() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			rw.Header().Add("Allow", "GET")
			rw.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		// try to retrieve the access token obtained through OIDC first and, if that doesn't exist,
		// fall back to the ID token issued by authenticating using the cluster-user-auth Secret. This way,
		// users can use both ways to log into weave-gitops.
		c, err := r.Cookie(AccessTokenCookieName)
		if err != nil {
			if err != http.ErrNoCookie {
				rw.WriteHeader(http.StatusBadRequest)
				return
			}

			c, err = r.Cookie(IDTokenCookieName)
			if err != nil {
				rw.WriteHeader(http.StatusBadRequest)
				return
			}
		}

		claims, err := s.tokenSignerVerifier.Verify(c.Value)
		if err == nil {
			ui := UserInfo{
				Email: claims.Subject,
			}
			toJson(rw, ui, s.Log)

			return
		}

		if !s.oidcEnabled() {
			ui := UserInfo{}
			toJson(rw, ui, s.Log)

			return
		}

		info, err := s.provider.UserInfo(r.Context(), oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: c.Value,
		}))
		if err != nil {
			JSONError(s.Log, rw, fmt.Sprintf("failed to query user info endpoint: %v", err), http.StatusUnauthorized)
			return
		}

		ui := UserInfo{
			Email: info.Email,
		}

		toJson(rw, ui, s.Log)
	}
}

func toJson(rw http.ResponseWriter, ui UserInfo, log logr.Logger) {
	b, err := json.Marshal(ui)
	if err != nil {
		JSONError(log, rw, fmt.Sprintf("failed to marshal to JSON: %v", err), http.StatusInternalServerError)
		return
	}

	_, err = rw.Write(b)
	if err != nil {
		log.Error(err, "Failing to write response")
	}
}

func (c *AuthServer) startAuthFlow(rw http.ResponseWriter, r *http.Request) {
	nonce, err := generateNonce()
	if err != nil {
		JSONError(c.Log, rw, fmt.Sprintf("failed to generate nonce: %v", err), http.StatusInternalServerError)
		return
	}

	returnUrl := r.URL.Query().Get("return_url")

	if returnUrl == "" {
		returnUrl = r.URL.String()
	}

	b, err := json.Marshal(SessionState{
		Nonce:     nonce,
		ReturnURL: returnUrl,
	})
	if err != nil {
		JSONError(c.Log, rw, fmt.Sprintf("failed to marshal state to JSON: %v", err), http.StatusInternalServerError)
		return
	}

	state := base64.StdEncoding.EncodeToString(b)

	var scopes []string
	// "openid", "email" and "groups" scopes added by default
	scopes = append(scopes, scopeProfile)
	authCodeUrl := c.oauth2Config(scopes).AuthCodeURL(state)

	// Issue state cookie
	http.SetCookie(rw, c.createCookie(StateCookieName, state))

	http.Redirect(rw, r, authCodeUrl, http.StatusSeeOther)
}

func (s *AuthServer) Logout() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			s.Log.Info("Only POST requests allowed")
			rw.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		http.SetCookie(rw, s.clearCookie(IDTokenCookieName))
		http.SetCookie(rw, s.clearCookie(AccessTokenCookieName))
		rw.WriteHeader(http.StatusOK)
	}
}

func (c *AuthServer) createCookie(name, value string) *http.Cookie {
	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().UTC().Add(c.config.TokenDuration),
		HttpOnly: true,
		Secure:   false,
	}

	return cookie
}

func (c *AuthServer) clearCookie(name string) *http.Cookie {
	cookie := &http.Cookie{
		Name:    name,
		Value:   "",
		Path:    "/",
		Expires: time.Unix(0, 0),
	}

	return cookie
}

// SessionState represents the state that needs to be persisted between
// the AuthN request from the Relying Party (RP) to the authorization
// endpoint of the OpenID Provider (OP) and the AuthN response back from
// the OP to the RP's callback URL. This state could be persisted server-side
// in a data store such as Redis but we prefer to operate stateless so we
// store this in a cookie instead. The cookie value and the value of the
// "state" parameter passed in the AuthN request are identical and set to
// the base64-encoded, JSON serialised state.
//
// https://openid.net/specs/openid-connect-core-1_0.html#Overview
// https://auth0.com/docs/configure/attack-protection/state-parameters#alternate-redirect-method
// https://community.auth0.com/t/state-parameter-and-user-redirection/8387/2
type SessionState struct {
	Nonce     string `json:"n"`
	ReturnURL string `json:"return_url"`
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}

	return false
}

func JSONError(log logr.Logger, w http.ResponseWriter, errStr string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)

	response := struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}{Message: errStr, Code: code}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error(err, "failed encoding error message", "message", errStr)
	}
}
