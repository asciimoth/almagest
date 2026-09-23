# admin_subscription_trees

## Setup

The case starts a real daemon with temporary storage and uses one persistent
Unix admin API connection. It does not require privileged Docker access.

## Actions and Expected Behavior

- Create nested KV and DNS subscriptions on one API session.
- Unsubscribe from a KV entry subscription and verify that it also removes its
  descendant value subscription, while the store subscription stays active.
- Unsubscribe from a DNS key subscription and verify that it also removes its
  descendant value subscription, while the cache subscription stays active.
- Unsubscribe from the remaining root subscriptions and verify that later
  mutations do not emit events for the removed subscription trees.
- Accept repeated unsubscribe requests for IDs that no longer exist.
- Explicitly subscribe and unsubscribe from the secrets and TLS surfaces.

