import { Page, Placeholder } from '../app/Page'

export function AdminSectionPage({ title, about }: { title: string; about: string }) {
  return (
    <Page title={title}>
      <Placeholder milestone="M0 (admin panel)">{about}</Placeholder>
    </Page>
  )
}
