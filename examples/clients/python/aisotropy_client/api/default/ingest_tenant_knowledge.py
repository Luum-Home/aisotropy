from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.error_model import ErrorModel
from ...models.knowledge_output_body import KnowledgeOutputBody
from ...models.knowledge_request import KnowledgeRequest
from typing import cast



def _get_kwargs(
    tenant_id: str,
    *,
    body: KnowledgeRequest,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/knowledge".format(tenant_id=quote(str(tenant_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> ErrorModel | KnowledgeOutputBody:
    if response.status_code == 200:
        response_200 = KnowledgeOutputBody.from_dict(response.json())



        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[ErrorModel | KnowledgeOutputBody]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    tenant_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: KnowledgeRequest,

) -> Response[ErrorModel | KnowledgeOutputBody]:
    """ Ingest documents into the tenant-shared semantic bucket (KindDocument, ADR-0019)

     Validates the contract, scans each document for prompt-injection payloads (stored/indirect injection
    guardrail — critical here because a tenant-shared doc reaches EVERY user of the tenant), chunks each
    document's content, embeds the chunks (BYO Embedder), and upserts them as KindDocument vectors
    scoped to a tenant-only, empty-user Scope (semantic.NewTenantScope) — visible to every user of
    tenant_id via the chat/chat-stream routes' semantic RAG hydration merge. Idempotent, exactly like
    the user-scoped POST .../user/{user_id}/knowledge.

    Args:
        tenant_id (str): Unique customer/tenant ID
        body (KnowledgeRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | KnowledgeOutputBody]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
body=body,

    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)

def sync(
    tenant_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: KnowledgeRequest,

) -> ErrorModel | KnowledgeOutputBody | None:
    """ Ingest documents into the tenant-shared semantic bucket (KindDocument, ADR-0019)

     Validates the contract, scans each document for prompt-injection payloads (stored/indirect injection
    guardrail — critical here because a tenant-shared doc reaches EVERY user of the tenant), chunks each
    document's content, embeds the chunks (BYO Embedder), and upserts them as KindDocument vectors
    scoped to a tenant-only, empty-user Scope (semantic.NewTenantScope) — visible to every user of
    tenant_id via the chat/chat-stream routes' semantic RAG hydration merge. Idempotent, exactly like
    the user-scoped POST .../user/{user_id}/knowledge.

    Args:
        tenant_id (str): Unique customer/tenant ID
        body (KnowledgeRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | KnowledgeOutputBody
     """


    return sync_detailed(
        tenant_id=tenant_id,
client=client,
body=body,

    ).parsed

async def asyncio_detailed(
    tenant_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: KnowledgeRequest,

) -> Response[ErrorModel | KnowledgeOutputBody]:
    """ Ingest documents into the tenant-shared semantic bucket (KindDocument, ADR-0019)

     Validates the contract, scans each document for prompt-injection payloads (stored/indirect injection
    guardrail — critical here because a tenant-shared doc reaches EVERY user of the tenant), chunks each
    document's content, embeds the chunks (BYO Embedder), and upserts them as KindDocument vectors
    scoped to a tenant-only, empty-user Scope (semantic.NewTenantScope) — visible to every user of
    tenant_id via the chat/chat-stream routes' semantic RAG hydration merge. Idempotent, exactly like
    the user-scoped POST .../user/{user_id}/knowledge.

    Args:
        tenant_id (str): Unique customer/tenant ID
        body (KnowledgeRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | KnowledgeOutputBody]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
body=body,

    )

    response = await client.get_async_httpx_client().request(
        **kwargs
    )

    return _build_response(client=client, response=response)

async def asyncio(
    tenant_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: KnowledgeRequest,

) -> ErrorModel | KnowledgeOutputBody | None:
    """ Ingest documents into the tenant-shared semantic bucket (KindDocument, ADR-0019)

     Validates the contract, scans each document for prompt-injection payloads (stored/indirect injection
    guardrail — critical here because a tenant-shared doc reaches EVERY user of the tenant), chunks each
    document's content, embeds the chunks (BYO Embedder), and upserts them as KindDocument vectors
    scoped to a tenant-only, empty-user Scope (semantic.NewTenantScope) — visible to every user of
    tenant_id via the chat/chat-stream routes' semantic RAG hydration merge. Idempotent, exactly like
    the user-scoped POST .../user/{user_id}/knowledge.

    Args:
        tenant_id (str): Unique customer/tenant ID
        body (KnowledgeRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | KnowledgeOutputBody
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
client=client,
body=body,

    )).parsed
