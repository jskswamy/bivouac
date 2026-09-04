package identity

import (
	"errors"
	"os/user"
	"testing"
)

func withCurrentUser(t *testing.T, u *user.User, err error) {
	t.Helper()
	orig := currentUser
	currentUser = func() (*user.User, error) { return u, err }
	t.Cleanup(func() { currentUser = orig })
}

func TestRemoteUser_UsesLocalUsernameLowercased(t *testing.T) {
	withCurrentUser(t, &user.User{Username: "Subramk"}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if got != "subramk" {
		t.Errorf("RemoteUser() = %q, want %q", got, "subramk")
	}
}

func TestRemoteUser_ReplacesInvalidCharsWithHyphen(t *testing.T) {
	withCurrentUser(t, &user.User{Username: "john.doe smith"}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if got != "john-doe-smith" {
		t.Errorf("RemoteUser() = %q, want %q", got, "john-doe-smith")
	}
}

func TestRemoteUser_StripsWindowsStyleDomainPrefix(t *testing.T) {
	withCurrentUser(t, &user.User{Username: `CORP\jdoe`}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if got != "jdoe" {
		t.Errorf("RemoteUser() = %q, want %q", got, "jdoe")
	}
}

func TestRemoteUser_PrefixesWhenStartingWithDigit(t *testing.T) {
	withCurrentUser(t, &user.User{Username: "123abc"}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if got != "u123abc" {
		t.Errorf("RemoteUser() = %q, want %q", got, "u123abc")
	}
}

func TestRemoteUser_TruncatesTo32Chars(t *testing.T) {
	withCurrentUser(t, &user.User{Username: "abcdefghijklmnopqrstuvwxyz1234567890"}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if len(got) > 32 {
		t.Errorf("RemoteUser() = %q, length %d, want <= 32", got, len(got))
	}
}

func TestRemoteUser_FallsBackToCloudlabWhenNothingUsableRemains(t *testing.T) {
	withCurrentUser(t, &user.User{Username: "!!!"}, nil)

	got, err := RemoteUser()
	if err != nil {
		t.Fatalf("RemoteUser() error = %v", err)
	}
	if got != "cloudlab" {
		t.Errorf("RemoteUser() = %q, want %q", got, "cloudlab")
	}
}

func TestRemoteUser_PropagatesUnderlyingError(t *testing.T) {
	boom := errors.New("boom")
	withCurrentUser(t, nil, boom)

	_, err := RemoteUser()
	if !errors.Is(err, boom) {
		t.Errorf("RemoteUser() error = %v, want %v", err, boom)
	}
}
