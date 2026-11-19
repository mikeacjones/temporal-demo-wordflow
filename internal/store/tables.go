package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// CreateTables creates the two tables as the DemoProject's spec.aws declares
// them, for DynamoDB Local and tests. On the platform the operator creates
// them; keep both definitions the same. A table that already exists is left
// alone.
func (s *Store) CreateTables(ctx context.Context) error {
	tables := []*dynamodb.CreateTableInput{
		{
			TableName:   aws.String(s.players),
			BillingMode: types.BillingModePayPerRequest,
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String(playerIDKey), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String(boardKey), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String(lifetimePointsKey), AttributeType: types.ScalarAttributeTypeN},
			},
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String(playerIDKey), KeyType: types.KeyTypeHash},
			},
			GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
				IndexName: aws.String(leaderboardIndex),
				KeySchema: []types.KeySchemaElement{
					{AttributeName: aws.String(boardKey), KeyType: types.KeyTypeHash},
					{AttributeName: aws.String(lifetimePointsKey), KeyType: types.KeyTypeRange},
				},
				Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
			}},
		},
		{
			TableName:   aws.String(s.campaigns),
			BillingMode: types.BillingModePayPerRequest,
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String(campaignIDKey), AttributeType: types.ScalarAttributeTypeS},
			},
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String(campaignIDKey), KeyType: types.KeyTypeHash},
			},
		},
	}
	for _, table := range tables {
		_, err := s.db.CreateTable(ctx, table)
		var inUse *types.ResourceInUseException
		if errors.As(err, &inUse) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create table %s: %w", *table.TableName, err)
		}
	}
	return nil
}
