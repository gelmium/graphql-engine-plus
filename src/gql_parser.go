package main

import (
	"regexp"

	"github.com/gofiber/fiber/v2"
)

type GraphQLRequest struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	OperationName string                 `json:"operationName"`
}

func ParseGraphQLRequest(c *fiber.Ctx) (*GraphQLRequest, error) {
	var req GraphQLRequest
	if err := c.BodyParser(&req); err != nil {
		return nil, err
	}
	// check if the query is empty
	if req.Query == "" {
		return nil, fiber.NewError(fiber.StatusBadRequest, "missing query in request body")
	}

	return &req, nil
}

var mutationRegex = regexp.MustCompile(`(?i)^\s*mutation\W`)
var subscriptionRegex = regexp.MustCompile(`(?i)^\s*subscription\W`)

func (req *GraphQLRequest) IsMutationGraphQLRequest() bool {
	// check if the query is a mutation with regex
	return mutationRegex.MatchString(req.Query)
}

func (req *GraphQLRequest) IsSubscriptionGraphQLRequest() bool {
	// check if the query is a subscription with regex
	return subscriptionRegex.MatchString(req.Query)
}
