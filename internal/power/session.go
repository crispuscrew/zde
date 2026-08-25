package power

import (
	"errors"

	"github.com/godbus/dbus/v5"
)

// Locked reads the hint Niri owns for this display session. It is deliberately
// a reading, not loginctl's LockSession request: asking clients to lock says
// nothing about whether a lock surface actually took the session.
func (l *Logind) Locked() (bool, error) {
	ctx, cancel := l.within()
	defer cancel()

	id := l.myID(ctx)
	if id == "" {
		return false, errors.New("logind cannot say which display session should be locked")
	}
	var path dbus.ObjectPath
	if err := l.call(ctx, mgrPath, mgrIface+".GetSession", []any{&path}, id); err != nil {
		return false, err
	}
	var value dbus.Variant
	if err := l.call(ctx, path, propsGet, []any{&value}, sessIface, "LockedHint"); err != nil {
		return false, err
	}
	var locked bool
	if err := value.Store(&locked); err != nil {
		return false, err
	}
	return locked, nil
}
