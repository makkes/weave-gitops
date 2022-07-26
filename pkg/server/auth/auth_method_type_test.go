package auth_test

import (
  "strings"
  "testing"

  "github.com/weaveworks/weave-gitops/pkg/server/auth"
)

func TestInvariant(t *testing.T) {
  authMethods := []auth.AuthMethod {auth.UserAccount, auth.OIDC, auth.TokenPassthrough }

  for _, method := range(authMethods){
    authstring := method.String()

    parsedMethod, err := auth.ParseAuthMethod(authstring)

    if err != nil {
      t.Fatalf("Auth methods should parse without error, got %s", err)
    }

    if parsedMethod != method {
      t.Fatalf("Parsing a stringified method should get the original method, expected %d, got %d", method, parsedMethod)
    }
  }
}

func TestBadAuthMethod(t *testing.T) {
  method, err := auth.ParseAuthMethod("badMethod")
  if !strings.HasPrefix(err.Error(), "Unknown auth method" ) {
    t.Fatalf("Expected ParseAuthMethod to produce 'Unknown auth method' error, instead got (method='%d', err='%s')", method, err)
  }
}
