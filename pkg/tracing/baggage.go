/*
   Copyright The Accelerated Container Image Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/baggage"
)

// Common baggage keys for the accelerated container image project
const (
	BaggageKeyRequestID   = "request.id"
	BaggageKeyUserID      = "user.id"
	BaggageKeySessionID   = "session.id"
	BaggageKeyOperation   = "operation"
	BaggageKeyImageName   = "image.name"
	BaggageKeyImageTag    = "image.tag"
	BaggageKeySnapshotKey = "snapshot.key"
	BaggageKeyContainerID = "container.id"
	BaggageKeyNamespace   = "namespace"
	BaggageKeyEnvironment = "environment"
)

// SetBaggageValue sets a baggage value in the context
func SetBaggageValue(ctx context.Context, key, value string) context.Context {
	member, err := baggage.NewMember(key, value)
	if err != nil {
		// Log error but don't fail the operation
		return ctx
	}

	bag := baggage.FromContext(ctx)
	bag, err = bag.SetMember(member)
	if err != nil {
		// Log error but don't fail the operation
		return ctx
	}

	return baggage.ContextWithBaggage(ctx, bag)
}

// GetBaggageValue gets a baggage value from the context
func GetBaggageValue(ctx context.Context, key string) string {
	bag := baggage.FromContext(ctx)
	member := bag.Member(key)
	return member.Value()
}

// SetRequestID sets the request ID in baggage
func SetRequestID(ctx context.Context, requestID string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyRequestID, requestID)
}

// GetRequestID gets the request ID from baggage
func GetRequestID(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyRequestID)
}

// SetUserID sets the user ID in baggage
func SetUserID(ctx context.Context, userID string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyUserID, userID)
}

// GetUserID gets the user ID from baggage
func GetUserID(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyUserID)
}

// SetOperation sets the operation name in baggage
func SetOperation(ctx context.Context, operation string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyOperation, operation)
}

// GetOperation gets the operation name from baggage
func GetOperation(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyOperation)
}

// SetImageInfo sets image name and tag in baggage
func SetImageInfo(ctx context.Context, name, tag string) context.Context {
	ctx = SetBaggageValue(ctx, BaggageKeyImageName, name)
	return SetBaggageValue(ctx, BaggageKeyImageTag, tag)
}

// GetImageInfo gets image name and tag from baggage
func GetImageInfo(ctx context.Context) (name, tag string) {
	return GetBaggageValue(ctx, BaggageKeyImageName), GetBaggageValue(ctx, BaggageKeyImageTag)
}

// SetSnapshotKey sets the snapshot key in baggage
func SetSnapshotKey(ctx context.Context, key string) context.Context {
	return SetBaggageValue(ctx, BaggageKeySnapshotKey, key)
}

// GetSnapshotKey gets the snapshot key from baggage
func GetSnapshotKey(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeySnapshotKey)
}

// SetContainerID sets the container ID in baggage
func SetContainerID(ctx context.Context, containerID string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyContainerID, containerID)
}

// GetContainerID gets the container ID from baggage
func GetContainerID(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyContainerID)
}

// SetNamespace sets the namespace in baggage
func SetNamespace(ctx context.Context, namespace string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyNamespace, namespace)
}

// GetNamespace gets the namespace from baggage
func GetNamespace(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyNamespace)
}

// SetEnvironment sets the environment in baggage
func SetEnvironment(ctx context.Context, environment string) context.Context {
	return SetBaggageValue(ctx, BaggageKeyEnvironment, environment)
}

// GetEnvironment gets the environment from baggage
func GetEnvironment(ctx context.Context) string {
	return GetBaggageValue(ctx, BaggageKeyEnvironment)
}

// SetBaggageFromMap sets multiple baggage values from a map
func SetBaggageFromMap(ctx context.Context, values map[string]string) context.Context {
	bag := baggage.FromContext(ctx)

	for key, value := range values {
		if value == "" {
			continue // Skip empty values
		}

		member, err := baggage.NewMember(key, value)
		if err != nil {
			continue // Skip invalid members
		}

		bag, err = bag.SetMember(member)
		if err != nil {
			continue // Skip if can't set member
		}
	}

	return baggage.ContextWithBaggage(ctx, bag)
}

// GetBaggageAsMap returns all baggage values as a map
func GetBaggageAsMap(ctx context.Context) map[string]string {
	bag := baggage.FromContext(ctx)
	result := make(map[string]string)

	for _, member := range bag.Members() {
		result[member.Key()] = member.Value()
	}

	return result
}
