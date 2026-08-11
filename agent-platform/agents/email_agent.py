"""
Email Agent — runs as a K8s CronJob every 30 min.

Read-only Gmail scope to start (see architecture doc). Never deletes
directly — proposes actions to the Approval Queue, or auto-labels for
categories judged low-risk and reversible.

Categories this agent recognizes: newsletter, receipt, action_needed, spam_ish
Auto-execute allowed for: newsletter, receipt (label + archive)
Always goes to approval: action_needed, spam_ish, anything unclassified
"""
import json
import os
import uuid

import requests
from google.oauth2.credentials import Credentials
from googleapiclient.discovery import build

APPROVAL_API = os.environ.get("APPROVAL_API", "http://approval-queue.agent-platform.svc:8000")
NATS_HTTP_BRIDGE = os.environ.get("NATS_HTTP_BRIDGE")  # or use nats-py directly, see note below

AUTO_EXECUTE_CATEGORIES = {"newsletter", "receipt"}
LABEL_MAP = {
    "newsletter": "Agent/Newsletter",
    "receipt": "Agent/Receipts",
    "action_needed": "Agent/NeedsAttention",
    "spam_ish": "Agent/ReviewSpam",
}


def get_gmail_service():
    creds = Credentials(
        token=None,
        refresh_token=os.environ["GMAIL_REFRESH_TOKEN"],
        client_id=os.environ["GMAIL_CLIENT_ID"],
        client_secret=os.environ["GMAIL_CLIENT_SECRET"],
        token_uri="https://oauth2.googleapis.com/token",
    )
    return build("gmail", "v1", credentials=creds)


def fetch_recent_unread(service, max_results=25):
    resp = service.users().messages().list(
        userId="me", q="is:unread newer_than:1d", maxResults=max_results
    ).execute()
    return resp.get("messages", [])


def get_message_summary(service, msg_id: str) -> dict:
    msg = service.users().messages().get(userId="me", id=msg_id, format="metadata",
                                          metadataHeaders=["From", "Subject"]).execute()
    headers = {h["name"]: h["value"] for h in msg["payload"]["headers"]}
    return {
        "id": msg_id,
        "from": headers.get("From", ""),
        "subject": headers.get("Subject", ""),
        "snippet": msg.get("snippet", ""),
    }


def classify_via_router(email_summary: dict) -> dict:
    """
    Request-reply against the router over NATS. Publishes to
    agent.tasks.> and waits on agent.results.<task_id>.
    (Using nats-py's request() here for simplicity instead of a raw
    pub + subscribe pair — same JetStream stream from router/main.py.)
    """
    import asyncio
    import nats

    async def _ask():
        nc = await nats.connect(os.environ.get("NATS_URL", "nats://nats.agent-platform.svc:4222"))
        task_id = str(uuid.uuid4())
        task = {
            "task_id": task_id,
            "task_type": "classify_email",
            "agent_name": "email_agent",
            "payload": {
                "text": f"From: {email_summary['from']}\nSubject: {email_summary['subject']}\n{email_summary['snippet']}",
                "categories": list(LABEL_MAP.keys()),
            },
        }
        response = await nc.request("agent.tasks.classify_email", json.dumps(task).encode(), timeout=15)
        await nc.close()
        return json.loads(response.data)

    return asyncio.run(_ask())


def propose_action(action_type: str, target_ref: str, payload: dict):
    requests.post(f"{APPROVAL_API}/proposed-actions", json={
        "agent_name": "email_agent",
        "action_type": action_type,
        "target_ref": target_ref,
        "payload": payload,
    }, timeout=10)


def apply_label(service, msg_id: str, category: str):
    label_name = LABEL_MAP[category]
    # Assumes labels already exist (create once via Gmail API or console).
    service.users().messages().modify(
        userId="me", id=msg_id, body={"addLabelIds": [label_name]}
    ).execute()


def run():
    service = get_gmail_service()
    messages = fetch_recent_unread(service)
    print(f"Found {len(messages)} unread messages to triage")

    for m in messages:
        summary = get_message_summary(service, m["id"])
        classification = classify_via_router(summary)
        category = classification["result"].strip().lower()

        if classification.get("degraded"):
            # Router couldn't verify quality (budget exhausted, low-confidence
            # local model) — don't trust an auto-action on a shaky classification.
            propose_action("review_classification", summary["id"],
                            {"summary": summary, "category_guess": category})
            continue

        if category in AUTO_EXECUTE_CATEGORIES:
            apply_label(service, summary["id"], category)
            print(f"Auto-labeled {summary['id']} as {category}")
        else:
            propose_action(f"label_as_{category}", summary["id"],
                            {"summary": summary, "category": category})
            print(f"Proposed action for {summary['id']} ({category}) — pending approval")


if __name__ == "__main__":
    run()
