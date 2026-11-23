/** RFC 9457 problem details, the error body for every non-2xx API response. */
export type Problem = {
  type: string
  title: string
  status: number
  detail?: string
  instance?: string
  /** Stable machine-readable code, e.g. "llm_unavailable". */
  code?: string
  [extension: string]: unknown
}

/** Thrown for every failed API call, so callers and TanStack Query see one error type. */
export class ApiError extends Error {
  override readonly name = 'ApiError'
  constructor(
    readonly status: number,
    readonly problem: Problem,
  ) {
    super(problem.detail ?? problem.title)
  }

  get code(): string | undefined {
    return this.problem.code
  }
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

/** Normalise any error body into a Problem, falling back to the HTTP status text. */
export function toProblem(response: Response, body: unknown): Problem {
  if (isRecord(body) && typeof body.title === 'string') {
    return {
      ...body,
      type: typeof body.type === 'string' ? body.type : 'about:blank',
      title: body.title,
      status: typeof body.status === 'number' ? body.status : response.status,
    }
  }
  return {
    type: 'about:blank',
    title: response.statusText || `HTTP ${response.status}`,
    status: response.status,
  }
}
