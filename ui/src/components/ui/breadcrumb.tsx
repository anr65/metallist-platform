import * as React from "react"
import { ChevronRight } from "lucide-react"
import { cn } from "cn"

function Breadcrumb({ ...props }: React.ComponentProps<"nav">) { return <nav aria-label="Навигация" data-slot="breadcrumb" {...props} /> }
function BreadcrumbList({ className, ...props }: React.ComponentProps<"ol">) { return <ol data-slot="breadcrumb-list" className={cn("flex min-w-0 flex-wrap items-center gap-1.5 text-sm text-muted-foreground", className)} {...props} /> }
function BreadcrumbItem({ className, ...props }: React.ComponentProps<"li">) { return <li data-slot="breadcrumb-item" className={cn("inline-flex min-w-0 items-center gap-1.5", className)} {...props} /> }
function BreadcrumbLink({ className, ...props }: React.ComponentProps<"button">) { return <button type="button" data-slot="breadcrumb-link" className={cn("rounded-sm transition-colors hover:text-foreground focus-visible:outline-2 focus-visible:outline-primary", className)} {...props} /> }
function BreadcrumbPage({ className, ...props }: React.ComponentProps<"span">) { return <span data-slot="breadcrumb-page" aria-current="page" className={cn("max-w-full truncate font-medium text-foreground", className)} {...props} /> }
function BreadcrumbSeparator({ children, className, ...props }: React.ComponentProps<"li">) { return <li role="presentation" aria-hidden="true" data-slot="breadcrumb-separator" className={cn("[&>svg]:size-3.5", className)} {...props}>{children ?? <ChevronRight />}</li> }

export { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbPage, BreadcrumbSeparator }
