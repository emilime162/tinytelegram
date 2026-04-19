#!/usr/bin/env bash
# Sync web-client/public/ to the EdgeStack S3 bucket and invalidate the
# CloudFront distribution. Bucket name + distribution ID are resolved from
# the deployed stack outputs so the script has no hard-coded IDs.
set -euo pipefail

BUCKET="$(aws cloudformation describe-stacks --stack-name TtEdgeStack \
  --query 'Stacks[0].Outputs[?OutputKey==`WebBucketName`].OutputValue' --output text)"
DIST_ID="$(aws cloudfront list-distributions \
  --query "DistributionList.Items[?Origins.Items[?contains(DomainName, '${BUCKET}')]].Id" \
  --output text | head -1)"

echo "Syncing web-client/public/ -> s3://${BUCKET}/"
aws s3 sync web-client/public/ "s3://${BUCKET}/" --delete --cache-control 'public, max-age=300'

echo "Invalidating CloudFront distribution ${DIST_ID}"
aws cloudfront create-invalidation --distribution-id "${DIST_ID}" --paths '/*'
