package reflect

import (
	"context"
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

type testNumberType struct {
}

var _ attr.Type = testNumberType{}

func (t testNumberType) TerraformType(_ context.Context) tftypes.Type {
	return tftypes.Number
}

func (t testNumberType) ValueFromTerraform(_ context.Context, in tftypes.Value) (attr.Value, error) {
	if !in.Type().Is(tftypes.Number) {
		return nil, fmt.Errorf("unexpected type %s", tftypes.Number)
	}
	result := &testNumberValue{}
	if !in.IsKnown() {
		result.Unknown = true
		return result, nil
	}
	if in.IsNull() {
		result.Null = true
		return result, nil
	}
	err := in.As(&result.Value)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (t testNumberType) Equal(other attr.Type) bool {
	_, ok := other.(testNumberType)
	return ok
}

type testNumberValue struct {
	Unknown bool
	Null    bool
	Value   *big.Float
}

var _ attr.Value = &testNumberValue{}

func (t *testNumberValue) ToTerraformValue(_ context.Context) (interface{}, error) {
	if t.Unknown {
		return tftypes.UnknownValue, nil
	}
	if t.Null {
		return nil, nil
	}
	return t.Value, nil
}

func (t *testNumberValue) Equal(other attr.Value) bool {
	if t == nil && other == nil {
		return true
	}
	if t == nil || other == nil {
		return false
	}
	o, ok := other.(*testNumberValue)
	if !ok {
		return false
	}
	if t.Unknown != o.Unknown {
		return false
	}
	if t.Null != o.Null {
		return false
	}
	if t.Value.Cmp(o.Value) == 0 {
		return false
	}
	return true
}
