import { MapPin, Store } from 'lucide-react'
import { Link } from 'react-router'
import { Page } from '../../app/Page'
import { List, ListItem } from '../../ui'

export function SettingsPage() {
  return (
    <Page title="Settings">
      <List>
        <ListItem asChild leading={<Store />} title="Stores" subtitle="Where you shop">
          <Link to="/settings/stores" />
        </ListItem>
        <ListItem
          asChild
          leading={<MapPin />}
          title="Locations"
          subtitle="Fridge, freezer, pantry and more"
        >
          <Link to="/settings/locations" />
        </ListItem>
      </List>
    </Page>
  )
}
