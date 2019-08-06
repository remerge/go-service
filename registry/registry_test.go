package registry

import (
	"reflect"
	"testing"

	"github.com/d4l3k/messagediff"
	"github.com/remerge/cue"
	"github.com/remerge/cue/collector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	cue.Collect(cue.DEBUG, collector.Terminal{}.New())
}

// needed for testing interface targets
type A struct{}

func (a *A) M() {}

type IA interface{ M() }

func TestServiceRegistry(t *testing.T) {
	register := func(r *Registry, ctor interface{}) error {
		_, err := r.Register(ctor)
		return err
	}
	t.Run("ctor without dependencies", func(t *testing.T) {
		r := New()

		type A struct{}

		assert.NoError(t, register(r, func() (*A, error) {
			return &A{}, nil
		}))

		type Target struct {
			A *A
		}
		target := &Target{}

		require.NoError(t, r.RequestAndSet(&target.A))
		assert.NotNil(t, target.A)
	})

	t.Run("ctor with interface target", func(t *testing.T) {
		r := New()

		assert.NoError(t, register(r, func() (IA, error) {
			return &A{}, nil
		}))

		type Target struct {
			A IA
		}
		target := &Target{}

		require.NoError(t, r.RequestAndSet(&target.A))
		assert.NotNil(t, target.A)
		assert.NotPanics(t, func() { _ = target.A.(*A) })
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

		assert.NoError(t, register(r, func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, register(r, func(a *A) (*B, error) { return &B{A: a}, nil }))
		assert.NoError(t, register(r, func(b *B) (*C, error) { return &C{B: b}, nil }))
		assert.NoError(t, register(r, func(a *A, c *C) (*D, error) { return &D{A: a, C: c}, nil }))

		type Target struct {
			C *C
			D *D
		}

		target := &Target{}

		assert.NoError(t, r.RequestAndSet(&target.D))
		assert.NoError(t, r.RequestAndSet(&target.C))

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
		type B struct {
			*A
		}
		type C struct {
			*A
			*B
		}

		type RequiredByB struct {
			Params
			*A
		}

		type RequiredByC struct {
			Params
			*A
			*B
		}
		assert.NoError(t, register(r, func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, register(r, func(p *RequiredByB) (*B, error) { return &B{A: p.A}, nil }))
		assert.NoError(t, register(r, func(p *RequiredByC) (*C, error) { return &C{A: p.A, B: p.B}, nil }))

		type Target struct {
			*B
			*C
		}
		target := &Target{}

		assert.NoError(t, r.RequestAndSet(&target.C))
		assert.NoError(t, r.RequestAndSet(&target.B))
		require.NotNil(t, target.C)
		require.NotNil(t, target.C.A)
		require.NotNil(t, target.C.B)
		require.NotNil(t, target.C.B.A)
		require.NotNil(t, target.C.B.A)
		require.Equal(t, target.C.B, target.B)
	})

	t.Run("ctor with request stage extra parameters", func(t *testing.T) {
		r := New()

		type A struct{}
		type B struct {
			*A
			String string
			Int    int
		}

		assert.NoError(t, register(r, func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, register(r, func(a *A, i int, s string) (*B, error) { return &B{A: a, Int: i, String: s}, nil }))

		type Target struct{ *B }
		target := &Target{}

		assert.NoError(t, r.RequestAndSet(&target.B, 42, "hallo"))
		require.NotNil(t, target.B)
		require.NotNil(t, target.B.A)
		require.Equal(t, target.B.String, "hallo")
		require.Equal(t, target.B.Int, 42)
	})

	t.Run("ctor with request stage param object", func(t *testing.T) {
		r := New()

		type A struct{}
		type B struct {
			*A
			String string
			Int    int
		}
		type C struct {
			*B
			Float float64
		}

		type InstantiationTimeParamsForB struct {
			Int    int
			String string
		}

		type SomeNestedParameter struct {
			Float float64
		}
		type InstantiationTimeParamsForC struct {
			SomeNestedParameter
		}

		type InstantiationTimeParamsForD struct {
			Int int
		}

		type RequiredByB struct {
			Params
			InstantiationTimeParamsForB `registry:"lazy"`
			A                           *A
		}

		type RequiredByC struct {
			Params
			InstantiationTimeParamsForC `registry:"lazy"`
			B                           *B
		}

		type D struct {
			*C
			Optional *InstantiationTimeParamsForD
		}

		type RequiredByD struct {
			Params
			*InstantiationTimeParamsForD `registry:"lazy,allownil"`
			C                            *C
		}

		assert.NoError(t, register(r, func() (*A, error) { return &A{}, nil }))
		assert.NoError(t, register(r, func(p *RequiredByB) (*B, error) { return &B{A: p.A, Int: p.Int, String: p.String}, nil }))
		assert.NoError(t, register(r, func(p *RequiredByC) (*C, error) {
			return &C{B: p.B, Float: p.InstantiationTimeParamsForC.SomeNestedParameter.Float}, nil
		}))

		assert.NoError(t, register(r, func(p *RequiredByD) (*D, error) {
			return &D{C: p.C, Optional: p.InstantiationTimeParamsForD}, nil
		}))

		type Target struct{ *D }
		target := &Target{}

		assert.NoError(t, r.RequestAndSet(&target.D, InstantiationTimeParamsForC{SomeNestedParameter{1.23}}, InstantiationTimeParamsForB{42, "hallo"}))
		require.NotNil(t, target.D.C)
		require.Nil(t, target.D.Optional)
		require.NotNil(t, target.D.C.B)
		require.NotNil(t, target.D.C.B.A)
		require.Equal(t, target.D.C.B.String, "hallo")
		require.Equal(t, target.D.C.B.Int, 42)
		require.Equal(t, target.D.C.Float, 1.23)
		// assert.NoError(t, r.RequestAndSet(&target.B, InstantiationTimeParamsForB{42, "hallo"}))
		// require.NotNil(t, target.B)
		// require.NotNil(t, target.B.A)
		// require.Equal(t, target.B.String, "hallo")
		// require.Equal(t, target.B.Int, 42)
	})
	t.Run("ctor Call method", func(t *testing.T) {
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

		assert.NoError(t, register(r, func() (*A, error) { return &A{}, nil }))
		ctor, err := r.Register(func(p *RequiredByB) (*B, error) { return &B{A: p.A, Int: p.Int, String: p.String}, nil })
		assert.NoError(t, err)
		b, err := ctor(InstantiationTimeParamsForB{42, "hallo"})
		assert.NoError(t, err)
		assert.NotNil(t, b)
	})
}

type testEntry struct {
	values       []interface{}
	targetStruct interface{}
}
type _testStruct1 struct{ A int }
type _testStruct2 struct {
	A int
	B string
}
type _innerStruct struct{ A int }
type InnerStruct struct{ A int }
type _testStruct3 struct{ I _innerStruct }
type _testStruct4 struct{ InnerStruct }

func TestCreateParamStruct(t *testing.T) {
	for _, e := range []*testEntry{
		{[]interface{}{123}, &_testStruct1{A: 123}},
		{[]interface{}{123, "test"}, &_testStruct2{A: 123, B: "test"}},
		{[]interface{}{_innerStruct{123}}, &_testStruct3{I: _innerStruct{123}}},
		{[]interface{}{InnerStruct{123}}, &_testStruct4{InnerStruct{123}}},
	} {
		var vv []reflect.Value
		for _, v := range e.values {
			vv = append(vv, reflect.ValueOf(v))
		}
		typ := reflect.TypeOf(e.targetStruct)
		ps := createParamStruct(typ, vv)
		if !reflect.DeepEqual(e.targetStruct, ps.Interface()) {
			diff, _ := messagediff.PrettyDiff(e.targetStruct, ps.Interface())
			t.Errorf("objects (%s) don't match but should:\n%v\n", typ, diff)
		}
	}
}
