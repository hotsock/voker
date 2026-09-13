#!/usr/bin/env python3
"""Capture real ingress path semantics; requires Python 3 and the AWS CLI."""

import argparse
import json
from pathlib import Path
import subprocess
import urllib.error
import urllib.parse
import urllib.request


FIXTURE = Path(__file__).resolve().parents[2] / "vokerhttp/testdata/ingress-paths.json"
QUERY = "marker=path-probe&value=a%3Fb%25c%23d&repeat=one&repeat=two"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", default="default")
    parser.add_argument("--region", default="us-west-2")
    parser.add_argument("--stack-name", default="vokerhttp-aws-ingress-probe")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    stack = json.loads(subprocess.check_output([
        "aws", "--profile", args.profile, "--region", args.region,
        "cloudformation", "describe-stacks", "--stack-name", args.stack_name,
        "--output", "json",
    ]))["Stacks"][0]
    results = []
    failures = 0
    for endpoint in stack["Outputs"]:
        if not endpoint["OutputKey"].endswith("Endpoint"):
            continue
        for case in json.loads(FIXTURE.read_text()):
            path = case["sentPath"]
            http_api = endpoint["OutputKey"] == "APIGatewayV2BufferedEndpoint"
            rejected = http_api and case.get("httpAPIRejected", False)
            expected_path = case.get("httpAPIPath") if http_api else urllib.parse.unquote(path)
            url = endpoint["OutputValue"].rstrip("/") + path + "?" + QUERY
            result = {"endpoint": endpoint["OutputKey"], "sentPath": path}
            try:
                with urllib.request.urlopen(url, timeout=30) as response:
                    result["status"] = response.status
                    result["response"] = json.load(response)
                echo = result["response"]
                event = echo["event"]
                expected_event_path = expected_path if http_api else path
                result["wirePathPreserved"] = echo["path"] == urllib.parse.unquote(path)
                result["passed"] = (
                    not rejected and result["status"] == 201
                    and event.get("rawPath", event.get("path")) == expected_event_path
                    and echo["path"] == expected_path
                    and urllib.parse.parse_qs(echo["rawQuery"]) == urllib.parse.parse_qs(QUERY)
                    and urllib.parse.urlsplit(echo["requestUri"]).fragment == ""
                    and urllib.parse.unquote(urllib.parse.urlsplit(echo["requestUri"]).path) == echo["path"]
                )
            except urllib.error.HTTPError as exc:
                result.update(status=exc.code, error=exc.read().decode(), passed=rejected and exc.code == 400)
            except (urllib.error.URLError, TimeoutError, ValueError) as exc:
                result.update(error=str(exc), passed=False)
            failures += not result["passed"]
            results.append(result)
            note = " (AWS rejects this path)" if rejected else " (AWS changes this path)" if result.get("wirePathPreserved") is False else ""
            print(f'{result["endpoint"]} {path}: {"PASS" if result["passed"] else "FAIL"}{note}', flush=True)
    args.output.write_text(json.dumps(results, indent=2, ensure_ascii=False) + "\n")
    print(f"{len(results) - failures}/{len(results)} passed; captures: {args.output}")
    raise SystemExit(bool(failures) or not results)


if __name__ == "__main__":
    main()
