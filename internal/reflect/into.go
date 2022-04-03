package reflect

import (
	"context"
	"fmt"
	"math/big"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Into uses the data in `val` to populate `target`, using the reflection
// package to recursively reflect into structs and slices. If `target` is an
// AttributeValue, its assignment method will be used instead of reflecting. If
// `target` is a tftypes.ValueConverter, the FromTerraformValue method will be
// used instead of using reflection. Primitives are set using the val.As
// method. Structs use reflection: each exported struct field must have a
// "tfsdk" tag with the name of the field in the tftypes.Value, and all fields
// in the tftypes.Value must have a corresponding property in the struct. Into
// will be called for each struct field. Slices will have Into called for each
// element.
func Into(ctx context.Context, typ attr.Type, val tftypes.Value, target interface{}, opts Options) diag.Diagnostics {
	var diags diag.Diagnostics

	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr {
		suggestedPointer := reflect.PointerTo(predictTargetType(ctx, typ, v))
		err := fmt.Errorf("target must be a pointer, got %T, which is a %s. Maybe change to %s", target, v.Kind(), suggestedPointer)
		diags.AddError(
			"Value Conversion Error",
			"An unexpected error was encountered trying to convert the value. This is always an error in the provider. Please report the following to the provider developer:\n\n"+err.Error(),
		)
		return diags
	}
	result, diags := BuildValue(ctx, typ, val, v.Elem(), opts, tftypes.NewAttributePath())
	if diags.HasError() {
		return diags
	}
	v.Elem().Set(result)
	return diags
}

// BuildValue constructs a reflect.Value of the same type as `target`,
// populated with the data in `val`. It will defensively instantiate new values
// to set, making it safe for use with pointer types which may be nil. It tries
// to give consumers the ability to override its default behaviors wherever
// possible.
func BuildValue(ctx context.Context, typ attr.Type, val tftypes.Value, target reflect.Value, opts Options, path *tftypes.AttributePath) (reflect.Value, diag.Diagnostics) {
	var diags diag.Diagnostics

	// if this isn't a valid reflect.Value, bail before we accidentally
	// panic
	if !target.IsValid() {
		err := fmt.Errorf("invalid target")
		diags.AddAttributeError(
			path,
			"Value Conversion Error",
			"An unexpected error was encountered trying to build a value. This is always an error in the provider. Please report the following to the provider developer:\n\n"+err.Error(),
		)
		return target, diags
	}
	// if this is an attr.Value, build the type from that
	if target.Type().Implements(reflect.TypeOf((*attr.Value)(nil)).Elem()) {
		return NewAttributeValue(ctx, typ, val, target, opts, path)
	}
	// if this tells tftypes how to build an instance of it out of a
	// tftypes.Value, well, that's what we want, so do that instead of our
	// default logic.
	if target.Type().Implements(reflect.TypeOf((*tftypes.ValueConverter)(nil)).Elem()) {
		return NewValueConverter(ctx, typ, val, target, opts, path)
	}
	// if this can explicitly be set to unknown, do that
	if target.Type().Implements(reflect.TypeOf((*Unknownable)(nil)).Elem()) {
		res, unknownableDiags := NewUnknownable(ctx, typ, val, target, opts, path)
		diags.Append(unknownableDiags...)
		if diags.HasError() {
			return target, diags
		}
		target = res
		// only return if it's unknown; we want to call SetUnknown
		// either way, but if the value is unknown, there's nothing
		// else to do, so bail
		if !val.IsKnown() {
			return target, nil
		}
	}
	// if this can explicitly be set to null, do that
	if target.Type().Implements(reflect.TypeOf((*Nullable)(nil)).Elem()) {
		res, nullableDiags := NewNullable(ctx, typ, val, target, opts, path)
		diags.Append(nullableDiags...)
		if diags.HasError() {
			return target, diags
		}
		target = res
		// only return if it's null; we want to call SetNull either
		// way, but if the value is null, there's nothing else to do,
		// so bail
		if val.IsNull() {
			return target, nil
		}
	}
	if !val.IsKnown() {
		// we already handled unknown the only ways we can
		// we checked that target doesn't have a SetUnknown method we
		// can call
		// we checked that target isn't an AttributeValue
		// all that's left to us now is to set it as an empty value or
		// throw an error, depending on what's in opts
		if !opts.UnhandledUnknownAsEmpty {
			err := fmt.Errorf("unhandled unknown value")
			diags.AddAttributeError(
				path,
				"Value Conversion Error",
				"An unexpected error was encountered trying to build a value. This is always an error in the provider. Please report the following to the provider developer:\n\n"+err.Error(),
			)
			return target, diags
		}
		// we want to set unhandled unknowns to the empty value
		return reflect.Zero(target.Type()), diags
	}

	if val.IsNull() {
		// we already handled null the only ways we can
		// we checked that target doesn't have a SetNull method we can
		// call
		// we checked that target isn't an AttributeValue
		// all that's left to us now is to set it as an empty value or
		// throw an error, depending on what's in opts
		if canBeNil(target) || opts.UnhandledNullAsEmpty {
			return reflect.Zero(target.Type()), nil
		}

		err := fmt.Errorf("unhandled null value")
		diags.AddAttributeError(
			path,
			"Value Conversion Error",
			"An unexpected error was encountered trying to build a value. This is always an error in the provider. Please report the following to the provider developer:\n\n"+err.Error(),
		)
		return target, diags
	}
	// *big.Float and *big.Int are technically pointers, but we want them
	// handled as numbers
	if target.Type() == reflect.TypeOf(big.NewFloat(0)) || target.Type() == reflect.TypeOf(big.NewInt(0)) {
		return Number(ctx, typ, val, target, opts, path)
	}
	switch target.Kind() {
	case reflect.Struct:
		val, valDiags := Struct(ctx, typ, val, target, opts, path)
		diags.Append(valDiags...)
		return val, diags
	case reflect.Bool, reflect.String:
		val, valDiags := Primitive(ctx, typ, val, target, path)
		diags.Append(valDiags...)
		return val, diags
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		// numbers are the wooooorst and need their own special handling
		// because we can't just hand them off to tftypes and also
		// because we can't just make people use *big.Floats, because a
		// nil *big.Float will crash everything if we don't handle it
		// as a special case, so let's just special case numbers and
		// let people use the types they want
		val, valDiags := Number(ctx, typ, val, target, opts, path)
		diags.Append(valDiags...)
		return val, diags
	case reflect.Slice:
		val, valDiags := reflectSlice(ctx, typ, val, target, opts, path)
		diags.Append(valDiags...)
		return val, diags
	case reflect.Map:
		val, valDiags := Map(ctx, typ, val, target, opts, path)
		diags.Append(valDiags...)
		return val, diags
	case reflect.Ptr:
		val, valDiags := Pointer(ctx, typ, val, target, opts, path)
		diags.Append(valDiags...)
		return val, diags
	default:
		err := fmt.Errorf("don't know how to reflect %s into %s", val.Type(), target.Type())
		diags.AddAttributeError(
			path,
			"Value Conversion Error",
			"An unexpected error was encountered trying to build a value. This is always an error in the provider. Please report the following to the provider developer:\n\n"+err.Error(),
		)
		return target, diags
	}
}

// predictTargetType tries to do an educated guess, which Go type should be used to store
// Terraform value. If Terraform type does not provide sufficient information tries to
// guess based on provided target
func predictTargetType(ctx context.Context, typ attr.Type, targetValue reflect.Value) reflect.Type {
	tt := typ.TerraformType(ctx)
	if tt.Is(tftypes.Bool) {
		return reflect.TypeOf((*bool)(nil)).Elem()
	}
	if tt.Is(tftypes.String) {
		return reflect.TypeOf((*string)(nil)).Elem()
	}

	// TODO: In theory this could try to find all structs, which have sufficient
	// tfsdk tagged fields.
	return targetValue.Type()
}
