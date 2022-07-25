// A collection of helper functions to render a graph of kubernetes objects
// with in the context of their parent-child relationships.
import _ from "lodash";
import { Core } from "./api/core/core.pb";
import {
  FluxObjectKind,
  GroupVersionKind,
  UnstructuredObject,
} from "./api/core/types.pb";

export type UnstructuredObjectWithChildren = UnstructuredObject & {
  children?: UnstructuredObjectWithChildren[];
};

// Kubernetes does not allow us to query children by parents.
// We keep a list of common parent-child relationships
// to look up children recursively.
export const PARENT_CHILD_LOOKUP = {
  Deployment: {
    group: "apps",
    version: "v1",
    kind: "Deployment",
    children: [
      {
        group: "apps",
        version: "v1",
        kind: "ReplicaSet",
        children: [{ version: "v1", kind: "Pod" }],
      },
    ],
  },
  StatefulSet: {
    group: "apps",
    version: "v1",
    kind: "StatefulSet",
    children: [{ version: "v1", kind: "Pod" }],
  },
};

export const getChildrenRecursive = async (
  client: typeof Core,
  namespace: string,

  object: UnstructuredObjectWithChildren,
  clusterName: string,
  lookup: any
) => {
  const children = [];

  const k = lookup[object.groupVersionKind.kind];

  if (k && k.children) {
    for (let i = 0; i < k.children.length; i++) {
      const child: GroupVersionKind = k.children[i];

      const res = await client.GetChildObjects({
        parentUid: object.uid,
        namespace,
        groupVersionKind: child,
        clusterName: clusterName,
      });

      for (let q = 0; q < res.objects.length; q++) {
        const c = res.objects[q];
        // Dive down one level and update the lookup accordingly.
        await getChildrenRecursive(client, namespace, c, clusterName, {
          [child.kind]: child,
        });
        children.push(c);
      }
    }
  }
  object.children = children;
};

// Gets the "child" objects that result from an Application
export const getChildren = async (
  client: typeof Core,
  automationName,
  namespace,
  automationKind: FluxObjectKind,
  kinds: GroupVersionKind[],
  clusterName
): Promise<UnstructuredObjectWithChildren[]> => {
  const { objects } = await client.GetReconciledObjects({
    automationName,
    namespace,
    automationKind,
    kinds,
    clusterName,
  });

  const result = [];
  for (let o = 0; o < objects.length; o++) {
    const obj = objects[o];

    await getChildrenRecursive(
      client,
      namespace,

      obj,
      clusterName,
      PARENT_CHILD_LOOKUP
    );
    result.push(obj);
  }
  return _.flatten(result);
};
