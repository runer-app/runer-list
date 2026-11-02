package limits

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProofOfWork(t *testing.T) {
	require.Equal(t, 0, leadingZeroBits([]byte{0x80}))
	require.Equal(t, 9, leadingZeroBits([]byte{0x00, 0x40}))
	require.Equal(t, 16, leadingZeroBits([]byte{0x00, 0x00}))

	authPub, nonce := []byte("pub"), []byte("nonce")
	var pow []byte
	for i := uint64(0); ; i++ {
		pow = binary.BigEndian.AppendUint64(nil, i)
		if VerifyProofOfWork(authPub, nonce, pow, 12) {
			break
		}
	}
	require.True(t, VerifyProofOfWork(authPub, nonce, pow, 8), "more work than required is fine")
	require.False(t, VerifyProofOfWork(authPub, []byte("other nonce"), pow, 12), "work is bound to the challenge")
}
