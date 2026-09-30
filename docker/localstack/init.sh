#!/bin/bash
# Provision SQS queues for the challenge (runs once when LocalStack starts).
set -e

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes '{
    "FifoQueue": "true",
    "ContentBasedDeduplication": "false",
    "VisibilityTimeout": "60",
    "RedrivePolicy": "{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo\",\"maxReceiveCount\":\"3\"}"
  }'

awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes '{"FifoQueue": "true", "ContentBasedDeduplication": "false"}'

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes '{"FifoQueue": "true", "ContentBasedDeduplication": "true"}'

echo "queues provisioned"