import { Breadcrumbs } from "@material-ui/core";
import _ from "lodash";
import React from "react";
import styled from "styled-components";
import useCommon from "../hooks/common";
import { formatURL } from "../lib/nav";
import { PageRoute, RequestError } from "../lib/types";
import Alert from "./Alert";
import Flex from "./Flex";
import Footer from "./Footer";
import Link from "./Link";
import LoadingPage from "./LoadingPage";
import Spacer from "./Spacer";

export type PageProps = {
  className?: string;
  children?: any;
  title?: string | JSX.Element;
  breadcrumbs?: { page: PageRoute; query?: any }[];
  actions?: JSX.Element;
  loading?: boolean;
  error?: RequestError | RequestError[];
};

export const Content = styled.div`
  min-height: 85vh;
  max-width: 1400px;
  margin: 0 auto;
  width: 100%;
  min-width: 1260px;
  box-sizing: border-box;
  background-color: rgba(255, 255, 255, 0.75);
  padding-left: ${(props) => props.theme.spacing.large};
  padding-right: ${(props) => props.theme.spacing.large};
  padding-top: ${(props) => props.theme.spacing.large};
  padding-bottom: ${(props) => props.theme.spacing.medium};
  border-radius: 10px;
`;

const Children = styled.div``;

export const TitleBar = styled.div`
  display: flex;
  width: 100%;
  justify-content: space-between;
  align-items: center;
  margin-bottom: ${(props) => props.theme.spacing.small};

  h2 {
    margin: 0 !important;
    color: ${(props) => props.theme.colors.neutral40} !important;
  }
`;

function pageLookup(p: PageRoute) {
  switch (p) {
    case PageRoute.Applications:
      return "Applications";

    default:
      break;
  }
}

function Errors({ error }) {
  const arr = _.isArray(error) ? error : [error];
  return (
    <>
      {_.map(arr, (e, i) => (
        <Flex key={i} center wide>
          <Alert title="Error" message={e?.message} severity="error" />
        </Flex>
      ))}
    </>
  );
}

function Page({
  className,
  children,
  title,
  breadcrumbs,
  actions,
  loading,
  error,
}: PageProps) {
  const { settings } = useCommon();

  if (loading) {
    return (
      <Content>
        <LoadingPage />
      </Content>
    );
  }

  return (
    <div className={className}>
      <Content>
        <TitleBar>
          <Breadcrumbs>
            {breadcrumbs &&
              _.map(breadcrumbs, (b) => (
                <Link key={b.page} to={formatURL(b.page, b.query)}>
                  <h2>{pageLookup(b.page)}</h2>
                </Link>
              ))}
            <h2>{title}</h2>
          </Breadcrumbs>
          {actions}
        </TitleBar>
        {error && <Errors error={error} />}
        <Spacer m={["small"]} />
        <Children>{children}</Children>
      </Content>
      {settings.renderFooter && <Footer />}
    </div>
  );
}

export default styled(Page)`
  min-height: 1216px;
  .MuiAlert-root {
    width: 100%;
  }
`;
