package auth

import (
	"context"
	"log"
)

// SeedDevKeys creates default API keys for development
func SeedDevKeys(ctx context.Context, store Store) error {
	// Check if any keys exist
	allKeys, err := store.ListAllKeys(ctx)
	if err != nil {
		return err
	}

	if len(allKeys) > 0 {
		log.Printf("Auth keys already exist (%d keys), skipping seed", len(allKeys))
		return nil
	}

	log.Println("Seeding development API keys...")

	// Create client key
	clientKey, err := GenerateAPIKey("Dev Client", "dev-client", KeyTypeClient)
	if err != nil {
		return err
	}
	if err := store.CreateKey(ctx, clientKey); err != nil {
		return err
	}
	log.Printf("Created client key: %s (owner: %s)", clientKey.Key, clientKey.OwnerID)

	// Create worker key
	workerKey, err := GenerateAPIKey("Dev Worker", "dev-worker", KeyTypeWorker)
	if err != nil {
		return err
	}
	if err := store.CreateKey(ctx, workerKey); err != nil {
		return err
	}
	log.Printf("Created worker key: %s (owner: %s)", workerKey.Key, workerKey.OwnerID)

	// Create admin key
	adminKey, err := GenerateAPIKey("Dev Admin", "dev-admin", KeyTypeAdmin)
	if err != nil {
		return err
	}
	if err := store.CreateKey(ctx, adminKey); err != nil {
		return err
	}
	log.Printf("Created admin key: %s (owner: %s)", adminKey.Key, adminKey.OwnerID)

	log.Println("Development keys seeded successfully")
	log.Println("⚠️  WARNING: These are development keys. Rotate them in production!")

	return nil
}
