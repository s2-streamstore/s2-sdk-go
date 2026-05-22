package s2_test

import (
	"context"
	"testing"

	"github.com/s2-streamstore/s2-sdk-go/s2"
)

func TestLocations_List(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: List locations")

	client := testClient(t)
	locations, err := client.Locations.List(ctx)
	if err != nil {
		t.Fatalf("List locations failed: %v", err)
	}
	if len(locations) == 0 {
		t.Fatal("Expected at least one location")
	}
	for _, location := range locations {
		if location.Name == "" {
			t.Fatalf("Expected non-empty location name: %#v", location)
		}
	}
}

func TestLocations_GetDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Get default location")

	client := testClient(t)
	location, err := client.Locations.GetDefault(ctx)
	if err != nil {
		t.Fatalf("Get default location failed: %v", err)
	}
	if location.Name == "" {
		t.Fatalf("Expected non-empty default location: %#v", location)
	}
}

func TestLocations_SetDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Set default location")

	client := testClient(t)
	current, err := client.Locations.GetDefault(ctx)
	if err != nil {
		t.Fatalf("Get default location failed: %v", err)
	}

	location, err := client.Locations.SetDefault(ctx, current.Name)
	if err != nil {
		t.Fatalf("Set default location failed: %v", err)
	}
	if location.Name != current.Name {
		t.Fatalf("Expected default location %s, got %s", current.Name, location.Name)
	}
	if location.IsPrivate != current.IsPrivate {
		t.Fatalf("Expected IsPrivate %v, got %v", current.IsPrivate, location.IsPrivate)
	}

	confirmed, err := client.Locations.GetDefault(ctx)
	if err != nil {
		t.Fatalf("Confirm default location failed: %v", err)
	}
	if confirmed.Name != current.Name {
		t.Fatalf("Expected confirmed default location %s, got %s", current.Name, confirmed.Name)
	}
}

func TestCreateBasin_WithDefaultLocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	t.Log("Testing: Create basin with the default location")

	client := testClient(t)
	defaultLocation, err := client.Locations.GetDefault(ctx)
	if err != nil {
		t.Fatalf("Get default location failed: %v", err)
	}

	basinName := uniqueBasinName("test-cdloc")
	defer deleteBasin(ctx, client, basinName)

	info, err := client.Basins.Create(ctx, s2.CreateBasinArgs{
		Basin: basinName,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if info.Location == nil || *info.Location != defaultLocation.Name {
		t.Fatalf("Expected default location %s, got %#v", defaultLocation.Name, info.Location)
	}
}
