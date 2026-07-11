# Live AWS fixtures

These CloudFormation custom resource events were captured from CloudWatch on
July 10, 2026 while deploying, updating, and deleting
`examples/cloudformation` in `us-west-2`.

The matching response fixtures contain the response objects produced by
Voker. Create and Update data were verified against the stack's `Fn::GetAtt`
outputs and physical resource ID; the Delete response was verified by the
stack reaching `DELETE_COMPLETE`.

Presigned response URLs, account IDs, and ARNs were sanitized. Request IDs,
property value types, lifecycle-specific fields, and response data retain the
values observed during the live deployment. In particular, a CloudFormation
`Number` parameter referenced from custom resource properties arrived as a
JSON string.
