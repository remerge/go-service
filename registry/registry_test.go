package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceRegistry(t *testing.T) {
	t.Run("ctor without dependencies", func(t *testing.T) {
		r := New()

		type A struct{}

		assert.NoError(t, r.Register(func() (*A, error) {
			return &A{}, nil
		}))

		type Target struct {
			A *A
		}
		target := &Target{}

		require.NoError(t, r.Request(&target.A))
		assert.NotNil(t, target.A)
	})

	t.Run("ctor with multiple deep dependencies", func(t *testing.T) {
		r := New()

		type A struct{}
		type B struct{ A *A }
		type C struct{ B *B }
		type D struct {
			A *A
			C *C
		}

		assert.NoError(t, r.Register(func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, r.Register(func(a *A) (*B, error) { return &B{A: a}, nil }))
		assert.NoError(t, r.Register(func(b *B) (*C, error) { return &C{B: b}, nil }))
		assert.NoError(t, r.Register(func(a *A, c *C) (*D, error) { return &D{A: a, C: c}, nil }))

		type Target struct {
			C *C
			D *D
		}

		target := &Target{}

		assert.NoError(t, r.Request(&target.D))
		assert.NoError(t, r.Request(&target.C))

		require.NotNil(t, target.C)
		require.NotNil(t, target.C.B)
		require.NotNil(t, target.C.B.A)
		require.NotNil(t, target.D.A)
		require.NotNil(t, target.D.C)
		assert.Equal(t, target.C, target.D.C)
	})

	t.Run("ctor with param object", func(t *testing.T) {
		r := New()

		type A struct{}
		type B struct{}
		type C struct {
			*A
			*B
		}

		type RequiredByC struct {
			Params
			*A
			*B
		}
		assert.NoError(t, r.Register(func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, r.Register(func() (*B, error) { return &B{}, nil }))
		assert.NoError(t, r.Register(func(p *RequiredByC) (*C, error) { return &C{A: p.A, B: p.B}, nil }))

		type Target struct{ *C }
		target := &Target{}

		assert.NoError(t, r.Request(&target.C))
		require.NotNil(t, target.C)
		require.NotNil(t, target.C.A)
		require.NotNil(t, target.C.B)
	})

	t.Run("ctor with request stage params", func(t *testing.T) {
		r := New()

		type A struct{}
		type B struct {
			*A
			String string
			Int    int
		}

		type InstantiationTimeParamsForB struct {
			Int    int
			String string
		}

		type RequiredByB struct {
			Params
			InstantiationTimeParamsForB `registry:"lazy"`
			A                           *A
		}

		assert.NoError(t, r.Register(func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, r.Register(func(p *RequiredByB) (*B, error) { return &B{A: p.A, Int: p.Int, String: p.String}, nil }))

		type Target struct{ *B }
		target := &Target{}

		assert.NoError(t, r.Request(&target.B, InstantiationTimeParamsForB{42, "hallo"}))
		require.NotNil(t, target.B)
		require.NotNil(t, target.B.A)
		require.Equal(t, target.B.String, "hallo")
		require.Equal(t, target.B.Int, 42)

	})
}
