from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.ask_output_body import AskOutputBody
from ...models.ask_request import AskRequest
from ...models.error_model import ErrorModel
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    *,
    body: AskRequest,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/ask".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> AskOutputBody | ErrorModel:
    if response.status_code == 200:
        response_200 = AskOutputBody.from_dict(response.json())



        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[AskOutputBody | ErrorModel]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: AskRequest,

) -> Response[AskOutputBody | ErrorModel]:
    """ Ask a stateless, response-cached question

     Answers a question WITHOUT any session state: no working-memory hydration, no conversation
    persistence, no consolidation outbox. It first consults a per-(tenant,user) response cache; on a hit
    the stored answer is returned WITHOUT an LLM call. On a miss it builds context from tenant-scoped
    semantic retrieval (RAG), makes one LLM call, runs the output guardrail, caches the answer under the
    (tenant,user) scope, and returns it. The cache is scoped so a cross-tenant hit is impossible by
    construction (see internal/responsecache).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AskRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AskOutputBody | ErrorModel]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)

def sync(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: AskRequest,

) -> AskOutputBody | ErrorModel | None:
    """ Ask a stateless, response-cached question

     Answers a question WITHOUT any session state: no working-memory hydration, no conversation
    persistence, no consolidation outbox. It first consults a per-(tenant,user) response cache; on a hit
    the stored answer is returned WITHOUT an LLM call. On a miss it builds context from tenant-scoped
    semantic retrieval (RAG), makes one LLM call, runs the output guardrail, caches the answer under the
    (tenant,user) scope, and returns it. The cache is scoped so a cross-tenant hit is impossible by
    construction (see internal/responsecache).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AskRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AskOutputBody | ErrorModel
     """


    return sync_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    ).parsed

async def asyncio_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: AskRequest,

) -> Response[AskOutputBody | ErrorModel]:
    """ Ask a stateless, response-cached question

     Answers a question WITHOUT any session state: no working-memory hydration, no conversation
    persistence, no consolidation outbox. It first consults a per-(tenant,user) response cache; on a hit
    the stored answer is returned WITHOUT an LLM call. On a miss it builds context from tenant-scoped
    semantic retrieval (RAG), makes one LLM call, runs the output guardrail, caches the answer under the
    (tenant,user) scope, and returns it. The cache is scoped so a cross-tenant hit is impossible by
    construction (see internal/responsecache).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AskRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AskOutputBody | ErrorModel]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = await client.get_async_httpx_client().request(
        **kwargs
    )

    return _build_response(client=client, response=response)

async def asyncio(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: AskRequest,

) -> AskOutputBody | ErrorModel | None:
    """ Ask a stateless, response-cached question

     Answers a question WITHOUT any session state: no working-memory hydration, no conversation
    persistence, no consolidation outbox. It first consults a per-(tenant,user) response cache; on a hit
    the stored answer is returned WITHOUT an LLM call. On a miss it builds context from tenant-scoped
    semantic retrieval (RAG), makes one LLM call, runs the output guardrail, caches the answer under the
    (tenant,user) scope, and returns it. The cache is scoped so a cross-tenant hit is impossible by
    construction (see internal/responsecache).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AskRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AskOutputBody | ErrorModel
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
