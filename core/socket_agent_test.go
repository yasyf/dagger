package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func testSSHAgentSocket(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		serve(conn)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("agent connection did not close")
		}
	})
	return path
}

func TestSSHAgentIdentities(t *testing.T) {
	t.Run("responsive", func(t *testing.T) {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		keyring := agent.NewKeyring()
		require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: private}))
		path := testSSHAgentSocket(t, func(conn net.Conn) { _ = agent.ServeAgent(keyring, conn) })
		keys, err := listSSHAgentIdentities(t.Context(), path)
		require.NoError(t, err)
		require.Len(t, keys, 1)
		want, err := ssh.NewPublicKey(public)
		require.NoError(t, err)
		require.Equal(t, want.Marshal(), keys[0].Blob)
	})
	t.Run("empty", func(t *testing.T) {
		path := testSSHAgentSocket(t, func(conn net.Conn) { _ = agent.ServeAgent(agent.NewKeyring(), conn) })
		keys, err := listSSHAgentIdentities(t.Context(), path)
		require.NoError(t, err)
		require.Empty(t, keys)
	})
	t.Run("cancel", func(t *testing.T) {
		request := make(chan struct{})
		closed := make(chan error, 1)
		path := testSSHAgentSocket(t, func(conn net.Conn) {
			var packet [5]byte
			_, err := io.ReadFull(conn, packet[:])
			if err != nil {
				closed <- err
				return
			}
			close(request)
			// Accept the request but never reply, as the stalled real agent does.
			var b [1]byte
			_, err = conn.Read(b[:])
			closed <- err
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := listSSHAgentIdentities(ctx, path)
			result <- err
		}()
		select {
		case <-request:
		case <-time.After(2 * time.Second):
			t.Fatal("agent did not receive identity request")
		}
		cancel()
		select {
		case err := <-result:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("identity request did not stop")
		}
		select {
		case err := <-closed:
			require.True(t, errors.Is(err, io.EOF), "connection was not closed: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("agent connection remained open")
		}
	})
	t.Run("protocol failure", func(t *testing.T) {
		path := testSSHAgentSocket(t, func(conn net.Conn) {
			var packet [5]byte
			if _, err := io.ReadFull(conn, packet[:]); err == nil {
				_, _ = conn.Write([]byte{0, 0, 0, 1, 5}) // SSH_AGENT_FAILURE
			}
		})
		keys, err := listSSHAgentIdentities(t.Context(), path)
		require.Error(t, err)
		require.Nil(t, keys)
	})
}
