package mail

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// GOA's well-known bus name and the root object that implements
// org.freedesktop.DBus.ObjectManager for every account it manages.
const (
	goaBusName  = "org.gnome.OnlineAccounts"
	goaRootPath = dbus.ObjectPath("/org/gnome/OnlineAccounts")

	goaAccountIface  = "org.gnome.OnlineAccounts.Account"
	goaMailIface     = "org.gnome.OnlineAccounts.Mail"
	goaOAuth2Iface   = "org.gnome.OnlineAccounts.OAuth2Based"
	goaPasswordIface = "org.gnome.OnlineAccounts.PasswordBased"
)

// goaDBusConn is the real goaBus, talking to the user's session bus. It
// is exercised only against a live GOA daemon (never in tests: the repo
// never dials a real bus), so keep it as thin as possible and put any
// logic worth testing in goa.go instead.
type goaDBusConn struct {
	conn *dbus.Conn
}

// NewGOASessionBus connects to the caller's D-Bus session bus for GOA
// discovery. The returned goaBus is meant for GOATokenSource/
// GOAPasswordSource; callers should Close the underlying connection
// (Conn) when done, e.g. via defer on the *dbus.Conn this wraps.
func NewGOASessionBus() (*goaDBusConn, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("mail: connect to session bus for goa: %w", err)
	}
	return &goaDBusConn{conn: conn}, nil
}

// managedObjects is GetManagedObjects' return shape: object path ->
// interface name -> property name -> value.
type managedObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (c *goaDBusConn) getManagedObjects(ctx context.Context) (managedObjects, error) {
	obj := c.conn.Object(goaBusName, goaRootPath)
	var objects managedObjects
	call := obj.CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0)
	if call.Err != nil {
		return nil, fmt.Errorf("mail: goa GetManagedObjects: %w", call.Err)
	}
	if err := call.Store(&objects); err != nil {
		return nil, fmt.Errorf("mail: decode goa GetManagedObjects: %w", err)
	}
	return objects, nil
}

// AccountPaths implements goaBus.
func (c *goaDBusConn) AccountPaths(ctx context.Context) ([]string, error) {
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(objects))
	for path, ifaces := range objects {
		if _, ok := ifaces[goaAccountIface]; ok {
			paths = append(paths, string(path))
		}
	}
	return paths, nil
}

// MailAccount implements goaBus.
func (c *goaDBusConn) MailAccount(ctx context.Context, path string) (GOAMailAccount, error) {
	objects, err := c.getManagedObjects(ctx)
	if err != nil {
		return GOAMailAccount{}, err
	}
	ifaces, ok := objects[dbus.ObjectPath(path)]
	if !ok {
		return GOAMailAccount{}, fmt.Errorf("mail: goa object %s not found", path)
	}
	acc := GOAMailAccount{
		Identity: variantString(ifaces[goaAccountIface]["PresentationIdentity"]),
		Host:     variantString(ifaces[goaMailIface]["ImapHost"]),
		User:     variantString(ifaces[goaMailIface]["ImapUserName"]),
	}
	return acc, nil
}

// AccessToken implements goaBus.
func (c *goaDBusConn) AccessToken(ctx context.Context, path string) (string, error) {
	obj := c.conn.Object(goaBusName, dbus.ObjectPath(path))
	call := obj.CallWithContext(ctx, goaOAuth2Iface+".GetAccessToken", 0)
	if call.Err != nil {
		return "", fmt.Errorf("mail: goa GetAccessToken(%s): %w", path, call.Err)
	}
	var token string
	var expiry int32
	if err := call.Store(&token, &expiry); err != nil {
		return "", fmt.Errorf("mail: decode goa GetAccessToken(%s): %w", path, err)
	}
	return token, nil
}

// Password implements goaBus.
func (c *goaDBusConn) Password(ctx context.Context, path string) (string, error) {
	obj := c.conn.Object(goaBusName, dbus.ObjectPath(path))
	call := obj.CallWithContext(ctx, goaPasswordIface+".GetPassword", 0)
	if call.Err != nil {
		return "", fmt.Errorf("mail: goa GetPassword(%s): %w", path, call.Err)
	}
	var password string
	if err := call.Store(&password); err != nil {
		return "", fmt.Errorf("mail: decode goa GetPassword(%s): %w", path, err)
	}
	return password, nil
}

func variantString(v dbus.Variant) string {
	s, _ := v.Value().(string)
	return s
}

var _ goaBus = (*goaDBusConn)(nil)

// lazyGOABus defers connecting to the session bus until a method is
// actually called, so building an Adapter (NewAdapter) never dials
// anything by itself.
type lazyGOABus struct {
	conn *goaDBusConn
	err  error
}

func (b *lazyGOABus) get() (*goaDBusConn, error) {
	if b.conn == nil && b.err == nil {
		b.conn, b.err = NewGOASessionBus()
	}
	return b.conn, b.err
}

func (b *lazyGOABus) AccountPaths(ctx context.Context) ([]string, error) {
	conn, err := b.get()
	if err != nil {
		return nil, err
	}
	return conn.AccountPaths(ctx)
}

func (b *lazyGOABus) MailAccount(ctx context.Context, path string) (GOAMailAccount, error) {
	conn, err := b.get()
	if err != nil {
		return GOAMailAccount{}, err
	}
	return conn.MailAccount(ctx, path)
}

func (b *lazyGOABus) AccessToken(ctx context.Context, path string) (string, error) {
	conn, err := b.get()
	if err != nil {
		return "", err
	}
	return conn.AccessToken(ctx, path)
}

func (b *lazyGOABus) Password(ctx context.Context, path string) (string, error) {
	conn, err := b.get()
	if err != nil {
		return "", err
	}
	return conn.Password(ctx, path)
}

var _ goaBus = (*lazyGOABus)(nil)
